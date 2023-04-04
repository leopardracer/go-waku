package static

import (
	"context"
	"errors"

	"github.com/waku-org/go-waku/waku/v2/protocol/rln/group_manager"
	"github.com/waku-org/go-zerokit-rln/rln"
	"go.uber.org/zap"
)

type StaticGroupManager struct {
	rln *rln.RLN
	log *zap.Logger

	identityCredential *rln.IdentityCredential
	membershipIndex    *rln.MembershipIndex

	group       []rln.IDCommitment
	rootTracker *group_manager.MerkleRootTracker
}

func NewStaticGroupManager(
	group []rln.IDCommitment,
	identityCredential rln.IdentityCredential,
	index rln.MembershipIndex,
	log *zap.Logger,
) (*StaticGroupManager, error) {
	// check the peer's index and the inclusion of user's identity commitment in the group
	if identityCredential.IDCommitment != group[int(index)] {
		return nil, errors.New("peer's IDCommitment does not match commitment in group")
	}

	return &StaticGroupManager{
		log:                log.Named("rln-static"),
		group:              group,
		identityCredential: &identityCredential,
		membershipIndex:    &index,
	}, nil
}

func (gm *StaticGroupManager) Start(ctx context.Context, rlnInstance *rln.RLN, rootTracker *group_manager.MerkleRootTracker) error {
	gm.log.Info("mounting rln-relay in off-chain/static mode")

	gm.rln = rlnInstance
	gm.rootTracker = rootTracker

	err := rootTracker.Sync()
	if err != nil {
		return err
	}

	// add members to the Merkle tree
	for _, member := range gm.group {
		if err := rlnInstance.InsertMember(member); err != nil {
			return err
		}

		err = rootTracker.Sync()
		if err != nil {
			return err
		}
	}

	gm.group = nil // Deleting group to release memory

	return nil
}

func (gm *StaticGroupManager) InsertMember(pubkey rln.IDCommitment) error {
	gm.log.Debug("a new key is added", zap.Binary("pubkey", pubkey[:]))
	// assuming all the members arrive in order
	err := gm.rln.InsertMember(pubkey)
	if err != nil {
		gm.log.Error("inserting member into merkletree", zap.Error(err))
		return err
	}

	err = gm.rootTracker.Sync()
	if err != nil {
		return err
	}

	return nil
}

func (gm *StaticGroupManager) IdentityCredentials() (rln.IdentityCredential, error) {
	if gm.identityCredential == nil {
		return rln.IdentityCredential{}, errors.New("identity credential has not been setup")
	}

	return *gm.identityCredential, nil
}

func (gm *StaticGroupManager) MembershipIndex() (rln.MembershipIndex, error) {
	if gm.membershipIndex == nil {
		return 0, errors.New("membership index has not been setup")
	}

	return *gm.membershipIndex, nil
}

func (gm *StaticGroupManager) Stop() {
	// Do nothing
}