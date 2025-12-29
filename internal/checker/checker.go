package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/sxwebdev/evm-node-check/internal/config"
)

type Options struct {
	MaxBlockGap    uint64
	BlockHashCount int
	CheckDebugMode bool
}

func DefaultOptions() Options {
	return Options{
		MaxBlockGap:    10,
		BlockHashCount: 5,
		CheckDebugMode: true,
	}
}

type NodeResult struct {
	ID          string
	Chain       string
	Address     string
	ChainID     *big.Int
	BlockNumber uint64
	BlockHashes map[uint64]common.Hash
	DebugOK     bool
	Error       error
}

type ChainResult struct {
	Chain           string
	Nodes           []NodeResult
	ExpectedChainID *big.Int
	MaxBlockNumber  uint64
	FailedNodes     []FailedNode
	Passed          bool
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

	// Check block hashes consistency
	c.checkBlockHashes(&result)

	return result
}

// blockHeader is a minimal block header for getting hash
type blockHeader struct {
	Hash common.Hash `json:"hash"`
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

	// Get chain ID
	chainID, err := ethClient.ChainID(ctx)
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
