package simulator

import (
	"testing"

	mock "github.com/prysmaticlabs/prysm/v3/beacon-chain/blockchain/testing"
	dbtest "github.com/prysmaticlabs/prysm/v3/beacon-chain/db/testing"
	mockstategen "github.com/prysmaticlabs/prysm/v3/beacon-chain/state/stategen/mock"
	types "github.com/prysmaticlabs/prysm/v3/consensus-types/primitives"
	"github.com/prysmaticlabs/prysm/v3/crypto/bls"
	ethpb "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/v3/testing/require"
	"github.com/prysmaticlabs/prysm/v3/testing/util"
)

func setupService(t *testing.T, params *Parameters) *Simulator {
	slasherDB := dbtest.SetupSlasherDB(t)
	beaconState, err := util.NewBeaconState()
	require.NoError(t, err)

	// We setup validators in the beacon state along with their
	// private keys used to generate valid signatures in generated objects.
	validators := make([]*ethpb.Validator, params.NumValidators)
	privKeys := make(map[types.ValidatorIndex]bls.SecretKey)
	for valIdx := range validators {
		privKey, err := bls.RandKey()
		require.NoError(t, err)
		privKeys[types.ValidatorIndex(valIdx)] = privKey
		validators[valIdx] = &ethpb.Validator{
			PublicKey:             privKey.PublicKey().Marshal(),
			WithdrawalCredentials: make([]byte, 32),
		}
	}
	err = beaconState.SetValidators(validators)
	require.NoError(t, err)

	gen := mockstategen.NewMockService()
	gen.AddStateForRoot(beaconState, [32]byte{})
	return &Simulator{
		srvConfig: &ServiceConfig{
			Params:                      params,
			Database:                    slasherDB,
			AttestationStateFetcher:     &mock.ChainService{State: beaconState},
			PrivateKeysByValidatorIndex: privKeys,
			StateGen:                    gen,
		},
	}
}
