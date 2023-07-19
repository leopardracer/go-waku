package rln

import (
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/waku-org/go-waku/waku/v2/protocol/pb"
	"github.com/waku-org/go-zerokit-rln/rln"
)

type messageValidationResult int

const (
	validationError messageValidationResult = iota
	validMessage
	invalidMessage
	spamMessage
)

// the maximum clock difference between peers in seconds
const maxClockGapSeconds = 20

// maximum allowed gap between the epochs of messages' RateLimitProofs
const maxEpochGap = int64(maxClockGapSeconds / rln.EPOCH_UNIT_SECONDS)

// acceptable roots for merkle root validation of incoming messages
const acceptableRootWindowSize = 5

type RegistrationHandler = func(tx *types.Transaction)

type SpamHandler = func(message *pb.WakuMessage) error

func toRLNSignal(wakuMessage *pb.WakuMessage) []byte {
	if wakuMessage == nil {
		return []byte{}
	}

	contentTopicBytes := []byte(wakuMessage.ContentTopic)
	return append(wakuMessage.Payload, contentTopicBytes...)
}

func toRateLimitProof(msg *pb.WakuMessage) *rln.RateLimitProof {
	if msg == nil || msg.RateLimitProof == nil {
		return nil
	}

	result := &rln.RateLimitProof{
		Proof:         rln.ZKSNARK(rln.Bytes128(msg.RateLimitProof.Proof)),
		MerkleRoot:    rln.MerkleNode(rln.Bytes32(msg.RateLimitProof.MerkleRoot)),
		Epoch:         rln.Epoch(rln.Bytes32(msg.RateLimitProof.Epoch)),
		ShareX:        rln.MerkleNode(rln.Bytes32(msg.RateLimitProof.ShareX)),
		ShareY:        rln.MerkleNode(rln.Bytes32(msg.RateLimitProof.ShareY)),
		Nullifier:     rln.Nullifier(rln.Bytes32(msg.RateLimitProof.Nullifier)),
		RLNIdentifier: rln.RLNIdentifier(rln.Bytes32(msg.RateLimitProof.RlnIdentifier)),
	}

	return result
}
