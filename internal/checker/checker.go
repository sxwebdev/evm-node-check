package checker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/sxwebdev/evm-node-check/internal/config"
	"golang.org/x/sync/errgroup"
)

type Options struct {
	MaxBlockGap    uint64
	BlockHashCount int
	CheckTimeout   time.Duration
	CheckDebugMode bool
	MaxBlockAge    time.Duration
	MinPeerCount   uint64
}

func DefaultOptions() Options {
	return Options{
		MaxBlockGap:    10,
		BlockHashCount: 5,
		CheckDebugMode: true,
		MaxBlockAge:    2 * time.Minute,
		MinPeerCount:   3,
	}
}

type BlockTxCounts struct {
	Block    uint64
	Eth      int
	Receipts int
	Debug    int
	Err      error
}

type NodeResult struct {
	ID                 string
	Chain              string
	Address            string
	ChainID            *big.Int
	BlockNumber        uint64
	BlockHashes        map[uint64]common.Hash
	HeadBlockTime      uint64
	Syncing            bool
	PeerCount          uint64
	PeerCountSupported bool
	DebugOK            bool
	Latency            time.Duration
	TxCounts           *BlockTxCounts
	Error              error
}

type ChainResult struct {
	Chain             string
	Nodes             []NodeResult
	ExpectedChainID   *big.Int
	MaxBlockNumber    uint64
	CommonBlockNumber uint64
	FailedNodes       []FailedNode
	Passed            bool
}

type CheckResult struct {
	ChainResults []ChainResult
	FailedNodes  []FailedNode
	Passed       bool
}

type FailedNode struct {
	ID      string
	Chain   string
	Address string
	Reason  string
}

type Checker struct {
	cfg    *config.Config
	opts   Options
	logger *slog.Logger
}

func New(cfg *config.Config, opts Options, logger *slog.Logger) *Checker {
	return &Checker{
		cfg:    cfg,
		opts:   opts,
		logger: logger,
	}
}

func (c *Checker) Check(ctx context.Context) (*CheckResult, error) {
	nodesByChain := c.cfg.GetNodesByChain()

	result := &CheckResult{
		ChainResults: make([]ChainResult, 0, len(nodesByChain)),
		FailedNodes:  make([]FailedNode, 0),
		Passed:       true,
	}

	for chain, nodes := range nodesByChain {
		chainResult := c.checkChain(ctx, chain, nodes)
		result.ChainResults = append(result.ChainResults, chainResult)

		if !chainResult.Passed {
			result.Passed = false
		}
		result.FailedNodes = append(result.FailedNodes, chainResult.FailedNodes...)
	}

	return result, nil
}

func (c *Checker) checkChain(ctx context.Context, chain string, nodes []config.NodeInfo) ChainResult {
	result := ChainResult{
		Chain:       chain,
		Nodes:       make([]NodeResult, len(nodes)),
		FailedNodes: make([]FailedNode, 0),
		Passed:      true,
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	// Gather info from all nodes in parallel
	for i, node := range nodes {
		wg.Add(1)
		go func(idx int, n config.NodeInfo) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(ctx, c.opts.CheckTimeout)
			defer cancel()

			info := c.checkNode(ctx, n)

			mu.Lock()
			result.Nodes[idx] = info
			mu.Unlock()
		}(i, node)
	}

	wg.Wait()

	// Determine expected chain ID (from first successful node)
	for _, node := range result.Nodes {
		if node.Error == nil && node.ChainID != nil {
			result.ExpectedChainID = node.ChainID
			break
		}
	}

	// Find max block number
	for _, node := range result.Nodes {
		if node.Error == nil && node.BlockNumber > result.MaxBlockNumber {
			result.MaxBlockNumber = node.BlockNumber
		}
	}

	// Validate all nodes
	for _, node := range result.Nodes {
		if node.Error != nil {
			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      node.ID,
				Chain:   node.Chain,
				Address: node.Address,
				Reason:  fmt.Sprintf("connection error: %v", node.Error),
			})
			result.Passed = false
			continue
		}

		// Check chain ID (only if we have expected chain ID)
		if result.ExpectedChainID != nil && node.ChainID.Cmp(result.ExpectedChainID) != 0 {
			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      node.ID,
				Chain:   node.Chain,
				Address: node.Address,
				Reason:  fmt.Sprintf("chain ID mismatch: expected %s, got %s", result.ExpectedChainID.String(), node.ChainID.String()),
			})
			result.Passed = false
			continue
		}

		// Check block gap
		if result.MaxBlockNumber-node.BlockNumber > c.opts.MaxBlockGap {
			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      node.ID,
				Chain:   node.Chain,
				Address: node.Address,
				Reason:  fmt.Sprintf("block gap too large: %d blocks behind (max allowed: %d)", result.MaxBlockNumber-node.BlockNumber, c.opts.MaxBlockGap),
			})
			result.Passed = false
			continue
		}

		// Check sync status
		if node.Syncing {
			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      node.ID,
				Chain:   node.Chain,
				Address: node.Address,
				Reason:  "node is still syncing (eth_syncing returned progress)",
			})
			result.Passed = false
			continue
		}

		// Check peer count (skip if chain/node does not expose net_peerCount)
		if node.PeerCountSupported && node.PeerCount < c.opts.MinPeerCount {
			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      node.ID,
				Chain:   node.Chain,
				Address: node.Address,
				Reason:  fmt.Sprintf("peer count too low: %d (min required: %d)", node.PeerCount, c.opts.MinPeerCount),
			})
			result.Passed = false
			continue
		}

		// Check head block freshness
		if c.opts.MaxBlockAge > 0 && node.HeadBlockTime > 0 {
			age := time.Since(time.Unix(int64(node.HeadBlockTime), 0))
			if age > c.opts.MaxBlockAge {
				result.FailedNodes = append(result.FailedNodes, FailedNode{
					ID:      node.ID,
					Chain:   node.Chain,
					Address: node.Address,
					Reason:  fmt.Sprintf("head block stale: age %s (max allowed: %s)", age.Truncate(time.Second), c.opts.MaxBlockAge),
				})
				result.Passed = false
				continue
			}
		}

		// Check debug mode
		if c.opts.CheckDebugMode && !node.DebugOK {
			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      node.ID,
				Chain:   node.Chain,
				Address: node.Address,
				Reason:  "debug mode not available (debug_traceBlockByNumber not supported)",
			})
			result.Passed = false
			continue
		}
	}

	// Check transaction count consistency on a common block
	c.checkBlockTransactions(ctx, &result)

	// Check block hashes consistency
	c.checkBlockHashes(&result)

	return result
}

// blockHeader is a minimal block header for getting hash and timestamp
type blockHeader struct {
	Hash      common.Hash    `json:"hash"`
	Timestamp hexutil.Uint64 `json:"timestamp"`
}

func (c *Checker) checkNode(ctx context.Context, n config.NodeInfo) NodeResult {
	info := NodeResult{
		ID:          n.ID,
		Chain:       n.Chain,
		Address:     n.Address,
		BlockHashes: make(map[uint64]common.Hash),
	}

	rpcClient, err := rpc.DialContext(ctx, n.Address)
	if err != nil {
		info.Error = fmt.Errorf("failed to connect: %w", err)
		return info
	}
	defer rpcClient.Close()

	ethClient := ethclient.NewClient(rpcClient)

	// Get chain ID and measure latency
	start := time.Now()
	chainID, err := ethClient.ChainID(ctx)
	info.Latency = time.Since(start)
	if err != nil {
		info.Error = fmt.Errorf("failed to get chain ID: %w", err)
		return info
	}
	info.ChainID = chainID

	// Get block number
	blockNumber, err := ethClient.BlockNumber(ctx)
	if err != nil {
		info.Error = fmt.Errorf("failed to get block number: %w", err)
		return info
	}
	info.BlockNumber = blockNumber

	// Check sync status. Some L2 clients (e.g. Arbitrum) return a non-nil
	// progress object even when fully caught up, populated with their own
	// non-standard fields. Only flag as syncing when CurrentBlock/HighestBlock
	// show a meaningful lag beyond MaxBlockGap.
	syncProgress, err := ethClient.SyncProgress(ctx)
	if err != nil {
		info.Error = fmt.Errorf("failed to get sync progress: %w", err)
		return info
	}
	if syncProgress != nil &&
		syncProgress.CurrentBlock > 0 &&
		syncProgress.HighestBlock > syncProgress.CurrentBlock &&
		syncProgress.HighestBlock-syncProgress.CurrentBlock > c.opts.MaxBlockGap {
		info.Syncing = true
	}

	// Get peer count (not all chains implement net_peerCount; some always return 0)
	var peerCountHex string
	if err := rpcClient.CallContext(ctx, &peerCountHex, "net_peerCount"); err != nil {
		var rerr rpc.Error
		if !errors.As(err, &rerr) || rerr.ErrorCode() != -32601 {
			info.Error = fmt.Errorf("failed to get peer count: %w", err)
			return info
		}
		c.logger.Debug("net_peerCount not supported, skipping peer count check", "node", n.ID)
	} else {
		peerCount, err := strconv.ParseUint(strings.TrimPrefix(peerCountHex, "0x"), 16, 64)
		if err != nil {
			info.Error = fmt.Errorf("failed to parse peer count %q: %w", peerCountHex, err)
			return info
		}
		info.PeerCount = peerCount
		if peerCount > 0 {
			info.PeerCountSupported = true
		} else {
			c.logger.Debug("net_peerCount returned 0, treating as unsupported", "node", n.ID)
		}
	}

	// Get block hashes for last N blocks using raw RPC calls
	for i := 0; i < c.opts.BlockHashCount; i++ {
		if blockNumber < uint64(i) {
			break
		}
		targetBlock := blockNumber - uint64(i)
		blockNumberHex := fmt.Sprintf("0x%x", targetBlock)

		var raw json.RawMessage
		err := rpcClient.CallContext(ctx, &raw, "eth_getBlockByNumber", blockNumberHex, false)
		if err != nil {
			c.logger.Warn("failed to get block",
				"node", n.ID,
				"block", targetBlock,
				"error", err)
			continue
		}

		if raw == nil {
			c.logger.Warn("block not found",
				"node", n.ID,
				"block", targetBlock)
			continue
		}

		var header blockHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			c.logger.Warn("failed to unmarshal block header",
				"node", n.ID,
				"block", targetBlock,
				"error", err)
			continue
		}

		info.BlockHashes[targetBlock] = header.Hash
		if i == 0 {
			info.HeadBlockTime = uint64(header.Timestamp)
		}
	}

	// Check debug mode
	if c.opts.CheckDebugMode {
		blockNumberHex := fmt.Sprintf("0x%x", blockNumber)
		var debugResult any
		err := rpcClient.CallContext(ctx, &debugResult, "debug_traceBlockByNumber", blockNumberHex, map[string]any{
			"tracer": "callTracer",
		})
		if err == nil {
			info.DebugOK = true
		} else {
			c.logger.Debug("debug API check failed",
				"node", n.ID,
				"error", err)
		}
	} else {
		info.DebugOK = true // Skip check
	}

	return info
}

func (c *Checker) checkBlockHashes(result *ChainResult) {
	// Build map of block number -> hash -> nodes that have this hash
	blockHashNodes := make(map[uint64]map[common.Hash][]string)

	for _, node := range result.Nodes {
		if node.Error != nil {
			continue
		}
		for blockNum, hash := range node.BlockHashes {
			if blockHashNodes[blockNum] == nil {
				blockHashNodes[blockNum] = make(map[common.Hash][]string)
			}
			blockHashNodes[blockNum][hash] = append(blockHashNodes[blockNum][hash], node.ID)
		}
	}

	// For each block, find the majority hash and report nodes with different hashes
	for blockNum, hashMap := range blockHashNodes {
		if len(hashMap) <= 1 {
			continue // All nodes agree
		}

		// Find majority hash
		var majorityHash common.Hash
		var maxCount int
		for hash, nodes := range hashMap {
			if len(nodes) > maxCount {
				maxCount = len(nodes)
				majorityHash = hash
			}
		}

		// Report nodes with different hashes
		for hash, nodeIDs := range hashMap {
			if hash == majorityHash {
				continue
			}
			for _, nodeID := range nodeIDs {
				// Find node info
				var nodeAddr, nodeChain string
				for _, n := range result.Nodes {
					if n.ID == nodeID {
						nodeAddr = n.Address
						nodeChain = n.Chain
						break
					}
				}

				result.FailedNodes = append(result.FailedNodes, FailedNode{
					ID:      nodeID,
					Chain:   nodeChain,
					Address: nodeAddr,
					Reason:  fmt.Sprintf("block hash mismatch at block %d: got %s, expected %s", blockNum, hash.Hex(), majorityHash.Hex()),
				})
				result.Passed = false
			}
		}
	}
}

func (c *Checker) checkBlockTransactions(ctx context.Context, result *ChainResult) {
	failedAddrs := make(map[string]struct{}, len(result.FailedNodes))
	for _, fn := range result.FailedNodes {
		failedAddrs[fn.Address] = struct{}{}
	}

	passingIdx := make([]int, 0, len(result.Nodes))
	var commonBlock uint64
	for i, node := range result.Nodes {
		if node.Error != nil {
			continue
		}
		if _, ok := failedAddrs[node.Address]; ok {
			continue
		}
		if len(passingIdx) == 0 || node.BlockNumber < commonBlock {
			commonBlock = node.BlockNumber
		}
		passingIdx = append(passingIdx, i)
	}

	if len(passingIdx) == 0 {
		return
	}
	if commonBlock == 0 {
		c.logger.Warn("skipping tx count check: common block is 0", "chain", result.Chain)
		return
	}

	result.CommonBlockNumber = commonBlock

	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, i := range passingIdx {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(ctx, c.opts.CheckTimeout)
			defer cancel()

			n := config.NodeInfo{
				ID:      result.Nodes[idx].ID,
				Chain:   result.Nodes[idx].Chain,
				Address: result.Nodes[idx].Address,
			}
			counts := c.fetchBlockTxCounts(ctx, n, commonBlock)

			mu.Lock()
			result.Nodes[idx].TxCounts = counts
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Per-node internal consistency check
	for _, i := range passingIdx {
		node := result.Nodes[i]
		if node.TxCounts == nil {
			continue
		}
		if node.TxCounts.Err != nil {
			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      node.ID,
				Chain:   node.Chain,
				Address: node.Address,
				Reason:  fmt.Sprintf("failed to fetch tx counts at block %d: %v", commonBlock, node.TxCounts.Err),
			})
			result.Passed = false
			failedAddrs[node.Address] = struct{}{}
			continue
		}
		if node.TxCounts.Eth != node.TxCounts.Receipts || node.TxCounts.Eth != node.TxCounts.Debug {
			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      node.ID,
				Chain:   node.Chain,
				Address: node.Address,
				Reason: fmt.Sprintf("transaction count mismatch at block %d: eth=%d, receipts=%d, debug=%d",
					commonBlock, node.TxCounts.Eth, node.TxCounts.Receipts, node.TxCounts.Debug),
			})
			result.Passed = false
			failedAddrs[node.Address] = struct{}{}
		}
	}

	// Cross-node majority vote on canonical count
	countNodes := make(map[int][]string)
	for _, i := range passingIdx {
		node := result.Nodes[i]
		if node.TxCounts == nil || node.TxCounts.Err != nil {
			continue
		}
		if _, ok := failedAddrs[node.Address]; ok {
			continue
		}
		countNodes[node.TxCounts.Eth] = append(countNodes[node.TxCounts.Eth], node.ID)
	}

	if len(countNodes) <= 1 {
		return
	}

	var majorityCount, maxLen int
	for count, nodes := range countNodes {
		if len(nodes) > maxLen {
			maxLen = len(nodes)
			majorityCount = count
		}
	}

	for count, nodeIDs := range countNodes {
		if count == majorityCount {
			continue
		}
		for _, nodeID := range nodeIDs {
			var nodeAddr, nodeChain string
			for _, n := range result.Nodes {
				if n.ID == nodeID {
					nodeAddr = n.Address
					nodeChain = n.Chain
					break
				}
			}

			result.FailedNodes = append(result.FailedNodes, FailedNode{
				ID:      nodeID,
				Chain:   nodeChain,
				Address: nodeAddr,
				Reason:  fmt.Sprintf("transaction count mismatch at block %d: got %d, majority %d", commonBlock, count, majorityCount),
			})
			result.Passed = false
		}
	}
}

func (c *Checker) fetchBlockTxCounts(ctx context.Context, n config.NodeInfo, block uint64) *BlockTxCounts {
	out := &BlockTxCounts{Block: block}

	rpcClient, err := rpc.DialContext(ctx, n.Address)
	if err != nil {
		out.Err = fmt.Errorf("failed to connect: %w", err)
		return out
	}
	defer rpcClient.Close()

	blockNumberHex := fmt.Sprintf("0x%x", block)

	var ethCount, receiptsCount, debugCount int

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		var raw json.RawMessage
		if err := rpcClient.CallContext(gctx, &raw, "eth_getBlockByNumber", blockNumberHex, true); err != nil {
			return fmt.Errorf("eth_getBlockByNumber: %w", err)
		}
		if len(raw) == 0 || string(raw) == "null" {
			return fmt.Errorf("eth_getBlockByNumber: block %d not found", block)
		}
		var body struct {
			Transactions []json.RawMessage `json:"transactions"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("eth_getBlockByNumber: unmarshal: %w", err)
		}
		ethCount = len(body.Transactions)
		return nil
	})

	g.Go(func() error {
		var raw json.RawMessage
		if err := rpcClient.CallContext(gctx, &raw, "eth_getBlockReceipts", blockNumberHex); err != nil {
			return fmt.Errorf("eth_getBlockReceipts: %w", err)
		}
		if len(raw) == 0 || string(raw) == "null" {
			return fmt.Errorf("eth_getBlockReceipts: block %d not found", block)
		}
		var receipts []json.RawMessage
		if err := json.Unmarshal(raw, &receipts); err != nil {
			return fmt.Errorf("eth_getBlockReceipts: unmarshal: %w", err)
		}
		receiptsCount = len(receipts)
		return nil
	})

	g.Go(func() error {
		var raw json.RawMessage
		err := rpcClient.CallContext(gctx, &raw, "debug_traceBlockByNumber", blockNumberHex, map[string]any{
			"tracer": "callTracer",
		})
		if err != nil {
			return fmt.Errorf("debug_traceBlockByNumber: %w", err)
		}
		if len(raw) == 0 || string(raw) == "null" {
			return fmt.Errorf("debug_traceBlockByNumber: block %d not found", block)
		}
		var traces []json.RawMessage
		if err := json.Unmarshal(raw, &traces); err != nil {
			return fmt.Errorf("debug_traceBlockByNumber: unmarshal: %w", err)
		}
		debugCount = len(traces)
		return nil
	})

	if err := g.Wait(); err != nil {
		out.Err = err
		return out
	}

	out.Eth = ethCount
	out.Receipts = receiptsCount
	out.Debug = debugCount
	return out
}
