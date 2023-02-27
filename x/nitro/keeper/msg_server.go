package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/sei-protocol/sei-chain/x/nitro/replay"
	"github.com/sei-protocol/sei-chain/x/nitro/types"
)

type msgServer struct {
	Keeper
}

func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

// allow whitelisted accounts to post L2 transaction data/state root onto Sei
func (server msgServer) RecordTransactionData(goCtx context.Context, msg *types.MsgRecordTransactionData) (*types.MsgRecordTransactionDataResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if !server.IsTxSenderWhitelisted(ctx, msg.Sender) {
		return nil, errors.New("sender account is not whitelisted to send nitro transaction data")
	}
	if existingSender, exists := server.GetSender(ctx, msg.Slot); exists {
		return nil, fmt.Errorf("slot %d has already been recorded by %s", msg.Slot, existingSender)
	}

	txsBz := [][]byte{}
	for _, tx := range msg.Txs {
		txBz, err := hex.DecodeString(tx)
		if err != nil {
			return nil, err
		}
		txsBz = append(txsBz, txBz)
	}
	server.SetTransactionData(ctx, msg.Slot, txsBz)
	stateRootBz, err := hex.DecodeString(msg.StateRoot)
	if err != nil {
		return nil, err
	}
	server.SetStateRoot(ctx, msg.Slot, stateRootBz)
	server.SetSender(ctx, msg.Slot, msg.Sender)

	return &types.MsgRecordTransactionDataResponse{}, nil
}

func (server msgServer) SubmitFraudChallenge(goCtx context.Context, msg *types.MsgSubmitFraudChallenge) (*types.MsgSubmitFraudChallengeResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if len(msg.FraudStatePubKey) == 0 {
		return nil, types.ErrInvalidFraudStatePubkey
	}

	// verify that the provided Merkle proof for the end slot is valid
	merkleRoot, err := server.GetStateRoot(ctx, msg.EndSlot)
	if err != nil {
		return nil, err
	}
	if err := server.Keeper.Validate(merkleRoot, msg.MerkleProof); err != nil {
		return nil, err
	}

	// read all recorded transactions between last good slot and first bad slot
	txsBz := [][]byte{}
	slot := msg.StartSlot
	for slot <= msg.EndSlot {
		bz, _ := server.GetTransactionData(ctx, slot)
		txsBz = append(txsBz, bz...)
		slot++
	}

	// execute Solana-bpf VM to obtain new account states
	newAccountStates, err := replay.Replay(ctx, txsBz, msg.AccountStates, msg.SysvarAccounts, msg.Programs)
	if err != nil {
		return nil, err
	}
	newAccountState := types.Account{}
	for _, state := range newAccountStates {
		if state.Pubkey == msg.FraudStatePubKey {
			newAccountState = state
			break
		}
	}

	if (types.Account{}) == newAccountState {
		return nil, types.ErrInvalidFraudStatePubkey
	}

	newAccountStateHash, err := AccountToValue(newAccountState)
	if err != nil {
		return nil, err
	}

	// check if the merkle root generated by the new account state and old merkle proof conflicts with the recorded merkle root
	msg.MerkleProof.Commitment = string(newAccountStateHash)
	if err := server.Keeper.Validate(merkleRoot, msg.MerkleProof); err != types.ErrInvalidMerkleProof {
		return nil, types.ErrInvalidMerkleProof
	}

	// TODO: charge gas fee if challenge fails

	return &types.MsgSubmitFraudChallengeResponse{}, nil
}
