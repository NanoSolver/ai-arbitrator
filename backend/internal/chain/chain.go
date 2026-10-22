package chain

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Chain is the oracle: it signs transactions with a single hot key and reads contract events.
// Only the plain ethclient + abi + types APIs are used, so no generated bindings are needed.
type Chain struct {
	client   *ethclient.Client
	abi      abi.ABI
	contract common.Address
	key      *ecdsa.PrivateKey
	from     common.Address
	chainID  *big.Int

	mu sync.Mutex // serialises sends so nonces never collide
}

type JobFunded struct {
	JobID      uint64
	Client     common.Address
	Freelancer common.Address
	Amount     *big.Int
	ReqHash    common.Hash
	Deadline   uint64
}

type WorkSubmitted struct {
	JobID   uint64
	SubHash common.Hash
	Attempt uint8
}

func New(ctx context.Context, rpcURL, contractAddr, privateKeyHex string, chainID int64) (*Chain, error) {
	if !common.IsHexAddress(contractAddr) {
		return nil, fmt.Errorf("CONTRACT_ADDRESS is not a valid address")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return nil, fmt.Errorf("ORACLE_PRIVATE_KEY is invalid (expect 64 hex chars): %w", err)
	}
	parsed, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		return nil, fmt.Errorf("parse abi: %w", err)
	}
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("dial rpc: %w", err)
	}
	got, err := client.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("rpc chain id: %w", err)
	}
	if got.Int64() != chainID {
		return nil, fmt.Errorf("RPC is on chain %d but CHAIN_ID=%d", got.Int64(), chainID)
	}
	return &Chain{
		client:   client,
		abi:      parsed,
		contract: common.HexToAddress(contractAddr),
		key:      key,
		from:     crypto.PubkeyToAddress(key.PublicKey),
		chainID:  got,
	}, nil
}

func (c *Chain) OracleAddress() common.Address   { return c.from }
func (c *Chain) ContractAddress() common.Address { return c.contract }

// ───────────────────────── Oracle actions ─────────────────────────

func (c *Chain) ReleaseFunds(ctx context.Context, jobID uint64) (common.Hash, error) {
	return c.send(ctx, "releaseFunds", jobID)
}

func (c *Chain) RejectSubmission(ctx context.Context, jobID uint64) (common.Hash, error) {
	return c.send(ctx, "rejectSubmission", jobID)
}

func (c *Chain) RefundClient(ctx context.Context, jobID uint64) (common.Hash, error) {
	return c.send(ctx, "refundClient", jobID)
}

func (c *Chain) send(ctx context.Context, method string, jobID uint64) (common.Hash, error) {
	data, err := c.abi.Pack(method, new(big.Int).SetUint64(jobID))
	if err != nil {
		return common.Hash{}, fmt.Errorf("pack %s: %w", method, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	nonce, err := c.client.PendingNonceAt(ctx, c.from)
	if err != nil {
		return common.Hash{}, fmt.Errorf("nonce: %w", err)
	}
	tip, err := c.client.SuggestGasTipCap(ctx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("gas tip: %w", err)
	}
	head, err := c.client.HeaderByNumber(ctx, nil)
	if err != nil {
		return common.Hash{}, fmt.Errorf("header: %w", err)
	}
	if head.BaseFee == nil {
		return common.Hash{}, errors.New("chain does not support EIP-1559 fees")
	}
	feeCap := new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tip)

	// EstimateGas simulates the call, so a revert (wrong state, not oracle, ...) fails here, before any gas is spent.
	gas, err := c.client.EstimateGas(ctx, ethereum.CallMsg{From: c.from, To: &c.contract, Data: data})
	if err != nil {
		return common.Hash{}, fmt.Errorf("%s would revert: %w", method, err)
	}

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   c.chainID,
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: feeCap,
		Gas:       gas * 12 / 10,
		To:        &c.contract,
		Value:     big.NewInt(0),
		Data:      data,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(c.chainID), c.key)
	if err != nil {
		return common.Hash{}, fmt.Errorf("sign: %w", err)
	}
	if err := c.client.SendTransaction(ctx, signed); err != nil {
		return common.Hash{}, fmt.Errorf("send: %w", err)
	}
	return signed.Hash(), nil
}

// Receipt reports whether the transaction is mined and, if so, whether it succeeded.
func (c *Chain) Receipt(ctx context.Context, h common.Hash) (found bool, success bool, err error) {
	r, err := c.client.TransactionReceipt(ctx, h)
	if errors.Is(err, ethereum.NotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, r.Status == types.ReceiptStatusSuccessful, nil
}

// ───────────────────────── Event reading ─────────────────────────

func (c *Chain) LatestBlock(ctx context.Context) (uint64, error) {
	return c.client.BlockNumber(ctx)
}

func (c *Chain) Logs(ctx context.Context, from, to uint64) ([]types.Log, error) {
	return c.client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(from),
		ToBlock:   new(big.Int).SetUint64(to),
		Addresses: []common.Address{c.contract},
		Topics:    [][]common.Hash{{c.abi.Events["JobFunded"].ID, c.abi.Events["WorkSubmitted"].ID}},
	})
}

// Decode turns a raw log into JobFunded or WorkSubmitted. Unknown logs return (nil, nil).
func (c *Chain) Decode(l types.Log) (any, error) {
	if len(l.Topics) == 0 {
		return nil, nil
	}
	switch l.Topics[0] {
	case c.abi.Events["JobFunded"].ID:
		if len(l.Topics) < 4 {
			return nil, fmt.Errorf("JobFunded: expected 4 topics, got %d", len(l.Topics))
		}
		vals, err := c.abi.Unpack("JobFunded", l.Data)
		if err != nil {
			return nil, fmt.Errorf("JobFunded unpack: %w", err)
		}
		if len(vals) != 3 {
			return nil, fmt.Errorf("JobFunded: expected 3 data fields, got %d", len(vals))
		}
		amount, ok1 := vals[0].(*big.Int)
		reqHash, ok2 := vals[1].([32]byte)
		deadline, ok3 := vals[2].(uint64)
		if !ok1 || !ok2 || !ok3 {
			return nil, errors.New("JobFunded: unexpected data types")
		}
		return JobFunded{
			JobID:      new(big.Int).SetBytes(l.Topics[1].Bytes()).Uint64(),
			Client:     common.BytesToAddress(l.Topics[2].Bytes()),
			Freelancer: common.BytesToAddress(l.Topics[3].Bytes()),
			Amount:     amount,
			ReqHash:    common.Hash(reqHash),
			Deadline:   deadline,
		}, nil

	case c.abi.Events["WorkSubmitted"].ID:
		if len(l.Topics) < 2 {
			return nil, fmt.Errorf("WorkSubmitted: expected 2 topics, got %d", len(l.Topics))
		}
		vals, err := c.abi.Unpack("WorkSubmitted", l.Data)
		if err != nil {
			return nil, fmt.Errorf("WorkSubmitted unpack: %w", err)
		}
		if len(vals) != 2 {
			return nil, fmt.Errorf("WorkSubmitted: expected 2 data fields, got %d", len(vals))
		}
		subHash, ok1 := vals[0].([32]byte)
		attempt, ok2 := vals[1].(uint8)
		if !ok1 || !ok2 {
			return nil, errors.New("WorkSubmitted: unexpected data types")
		}
		return WorkSubmitted{
			JobID:   new(big.Int).SetBytes(l.Topics[1].Bytes()).Uint64(),
			SubHash: common.Hash(subHash),
			Attempt: attempt,
		}, nil
	}
	return nil, nil
}
