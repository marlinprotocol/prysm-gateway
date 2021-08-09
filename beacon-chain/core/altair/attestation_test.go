package altair_test

import (
	"testing"

	types "github.com/prysmaticlabs/eth2-types"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/altair"
	"github.com/prysmaticlabs/prysm/beacon-chain/state"
	ethpb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/shared/mathutil"
	"github.com/prysmaticlabs/prysm/shared/params"
	"github.com/prysmaticlabs/prysm/shared/testutil"
	"github.com/prysmaticlabs/prysm/shared/testutil/require"
)

func TestValidatorFlag_Has(t *testing.T) {
	tests := []struct {
		name     string
		set      uint8
		expected []uint8
	}{
		{name: "none",
			set:      0,
			expected: []uint8{},
		},
		{
			name:     "source",
			set:      1,
			expected: []uint8{params.BeaconConfig().TimelySourceFlagIndex},
		},
		{
			name:     "target",
			set:      2,
			expected: []uint8{params.BeaconConfig().TimelyTargetFlagIndex},
		},
		{
			name:     "head",
			set:      4,
			expected: []uint8{params.BeaconConfig().TimelyHeadFlagIndex},
		},
		{
			name:     "source, target",
			set:      3,
			expected: []uint8{params.BeaconConfig().TimelySourceFlagIndex, params.BeaconConfig().TimelyTargetFlagIndex},
		},
		{
			name:     "source, head",
			set:      5,
			expected: []uint8{params.BeaconConfig().TimelySourceFlagIndex, params.BeaconConfig().TimelyHeadFlagIndex},
		},
		{
			name:     "target, head",
			set:      6,
			expected: []uint8{params.BeaconConfig().TimelyTargetFlagIndex, params.BeaconConfig().TimelyTargetFlagIndex},
		},
		{
			name:     "source, target, head",
			set:      7,
			expected: []uint8{params.BeaconConfig().TimelySourceFlagIndex, params.BeaconConfig().TimelyTargetFlagIndex, params.BeaconConfig().TimelyHeadFlagIndex},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, f := range tt.expected {
				require.Equal(t, true, altair.HasValidatorFlag(tt.set, f))
			}
		})
	}
}

func TestValidatorFlag_Add(t *testing.T) {
	tests := []struct {
		name          string
		set           []uint8
		expectedTrue  []uint8
		expectedFalse []uint8
	}{
		{name: "none",
			set:           []uint8{},
			expectedTrue:  []uint8{},
			expectedFalse: []uint8{params.BeaconConfig().TimelySourceFlagIndex, params.BeaconConfig().TimelyTargetFlagIndex, params.BeaconConfig().TimelyHeadFlagIndex},
		},
		{
			name:          "source",
			set:           []uint8{params.BeaconConfig().TimelySourceFlagIndex},
			expectedTrue:  []uint8{params.BeaconConfig().TimelySourceFlagIndex},
			expectedFalse: []uint8{params.BeaconConfig().TimelyTargetFlagIndex, params.BeaconConfig().TimelyHeadFlagIndex},
		},
		{
			name:          "source, target",
			set:           []uint8{params.BeaconConfig().TimelySourceFlagIndex, params.BeaconConfig().TimelyTargetFlagIndex},
			expectedTrue:  []uint8{params.BeaconConfig().TimelySourceFlagIndex, params.BeaconConfig().TimelyTargetFlagIndex},
			expectedFalse: []uint8{params.BeaconConfig().TimelyHeadFlagIndex},
		},
		{
			name:          "source, target, head",
			set:           []uint8{params.BeaconConfig().TimelySourceFlagIndex, params.BeaconConfig().TimelyTargetFlagIndex, params.BeaconConfig().TimelyHeadFlagIndex},
			expectedTrue:  []uint8{params.BeaconConfig().TimelySourceFlagIndex, params.BeaconConfig().TimelyTargetFlagIndex, params.BeaconConfig().TimelyHeadFlagIndex},
			expectedFalse: []uint8{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := uint8(0)
			for _, f := range tt.set {
				b = altair.AddValidatorFlag(b, f)
			}
			for _, f := range tt.expectedFalse {
				require.Equal(t, false, altair.HasValidatorFlag(b, f))
			}
			for _, f := range tt.expectedTrue {
				require.Equal(t, true, altair.HasValidatorFlag(b, f))
			}
		})
	}
}

func TestAttestationParticipationFlagIndices(t *testing.T) {
	beaconState, _ := testutil.DeterministicGenesisStateAltair(t, params.BeaconConfig().MaxValidatorsPerCommittee)
	require.NoError(t, beaconState.SetSlot(1))
	cfg := params.BeaconConfig()
	sourceFlagIndex := cfg.TimelySourceFlagIndex
	targetFlagIndex := cfg.TimelyTargetFlagIndex
	headFlagIndex := cfg.TimelyHeadFlagIndex

	tests := []struct {
		name                 string
		inputState           state.BeaconState
		inputData            *ethpb.AttestationData
		inputDelay           types.Slot
		participationIndices map[uint8]bool
	}{
		{
			name: "none",
			inputState: func() state.BeaconState {
				return beaconState
			}(),
			inputData: &ethpb.AttestationData{
				Source: &ethpb.Checkpoint{Root: params.BeaconConfig().ZeroHash[:]},
				Target: &ethpb.Checkpoint{},
			},
			inputDelay:           params.BeaconConfig().SlotsPerEpoch,
			participationIndices: map[uint8]bool{},
		},
		{
			name: "participated source",
			inputState: func() state.BeaconState {
				return beaconState
			}(),
			inputData: &ethpb.AttestationData{
				Source: &ethpb.Checkpoint{Root: params.BeaconConfig().ZeroHash[:]},
				Target: &ethpb.Checkpoint{},
			},
			inputDelay: types.Slot(mathutil.IntegerSquareRoot(uint64(cfg.SlotsPerEpoch)) - 1),
			participationIndices: map[uint8]bool{
				sourceFlagIndex: true,
			},
		},
		{
			name: "participated source and target",
			inputState: func() state.BeaconState {
				return beaconState
			}(),
			inputData: &ethpb.AttestationData{
				Source: &ethpb.Checkpoint{Root: params.BeaconConfig().ZeroHash[:]},
				Target: &ethpb.Checkpoint{Root: params.BeaconConfig().ZeroHash[:]},
			},
			inputDelay: types.Slot(mathutil.IntegerSquareRoot(uint64(cfg.SlotsPerEpoch)) - 1),
			participationIndices: map[uint8]bool{
				sourceFlagIndex: true,
				targetFlagIndex: true,
			},
		},
		{
			name: "participated source and target and head",
			inputState: func() state.BeaconState {
				return beaconState
			}(),
			inputData: &ethpb.AttestationData{
				BeaconBlockRoot: params.BeaconConfig().ZeroHash[:],
				Source:          &ethpb.Checkpoint{Root: params.BeaconConfig().ZeroHash[:]},
				Target:          &ethpb.Checkpoint{Root: params.BeaconConfig().ZeroHash[:]},
			},
			inputDelay: 1,
			participationIndices: map[uint8]bool{
				sourceFlagIndex: true,
				targetFlagIndex: true,
				headFlagIndex:   true,
			},
		},
	}

	for _, test := range tests {
		flagIndices, err := altair.AttestationParticipationFlagIndices(test.inputState, test.inputData, test.inputDelay)
		require.NoError(t, err)
		require.DeepEqual(t, test.participationIndices, flagIndices)
	}
}

func TestMatchingStatus(t *testing.T) {
	beaconState, _ := testutil.DeterministicGenesisStateAltair(t, params.BeaconConfig().MaxValidatorsPerCommittee)
	require.NoError(t, beaconState.SetSlot(1))
	tests := []struct {
		name          string
		inputState    state.BeaconState
		inputData     *ethpb.AttestationData
		inputCheckpt  *ethpb.Checkpoint
		matchedSource bool
		matchedTarget bool
		matchedHead   bool
	}{
		{
			name:       "non matched",
			inputState: beaconState,
			inputData: &ethpb.AttestationData{
				Source: &ethpb.Checkpoint{Epoch: 1},
				Target: &ethpb.Checkpoint{},
			},
			inputCheckpt: &ethpb.Checkpoint{},
		},
		{
			name:       "source matched",
			inputState: beaconState,
			inputData: &ethpb.AttestationData{
				Source: &ethpb.Checkpoint{},
				Target: &ethpb.Checkpoint{},
			},
			inputCheckpt:  &ethpb.Checkpoint{},
			matchedSource: true,
		},
		{
			name:       "target matched",
			inputState: beaconState,
			inputData: &ethpb.AttestationData{
				Source: &ethpb.Checkpoint{Epoch: 1},
				Target: &ethpb.Checkpoint{Root: params.BeaconConfig().ZeroHash[:]},
			},
			inputCheckpt:  &ethpb.Checkpoint{},
			matchedTarget: true,
		},
		{
			name:       "head matched",
			inputState: beaconState,
			inputData: &ethpb.AttestationData{
				Source:          &ethpb.Checkpoint{Epoch: 1},
				Target:          &ethpb.Checkpoint{},
				BeaconBlockRoot: params.BeaconConfig().ZeroHash[:],
			},
			inputCheckpt: &ethpb.Checkpoint{},
			matchedHead:  true,
		},
		{
			name:       "everything matched",
			inputState: beaconState,
			inputData: &ethpb.AttestationData{
				Source:          &ethpb.Checkpoint{},
				Target:          &ethpb.Checkpoint{Root: params.BeaconConfig().ZeroHash[:]},
				BeaconBlockRoot: params.BeaconConfig().ZeroHash[:],
			},
			inputCheckpt:  &ethpb.Checkpoint{},
			matchedSource: true,
			matchedTarget: true,
			matchedHead:   true,
		},
	}

	for _, test := range tests {
		src, tgt, head, err := altair.MatchingStatus(test.inputState, test.inputData, test.inputCheckpt)
		require.NoError(t, err)
		require.Equal(t, test.matchedSource, bool(src))
		require.Equal(t, test.matchedTarget, bool(tgt))
		require.Equal(t, test.matchedHead, bool(head))
	}
}
