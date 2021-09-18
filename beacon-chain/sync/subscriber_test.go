package sync

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	types "github.com/prysmaticlabs/eth2-types"
	"github.com/prysmaticlabs/prysm/async/abool"
	mockChain "github.com/prysmaticlabs/prysm/beacon-chain/blockchain/testing"
	"github.com/prysmaticlabs/prysm/beacon-chain/cache"
	"github.com/prysmaticlabs/prysm/beacon-chain/core"
	"github.com/prysmaticlabs/prysm/beacon-chain/core/helpers"
	db "github.com/prysmaticlabs/prysm/beacon-chain/db/testing"
	"github.com/prysmaticlabs/prysm/beacon-chain/operations/slashings"
	"github.com/prysmaticlabs/prysm/beacon-chain/p2p"
	"github.com/prysmaticlabs/prysm/beacon-chain/p2p/encoder"
	p2ptest "github.com/prysmaticlabs/prysm/beacon-chain/p2p/testing"
	mockSync "github.com/prysmaticlabs/prysm/beacon-chain/sync/initial-sync/testing"
	lruwrpr "github.com/prysmaticlabs/prysm/cache/lru"
	"github.com/prysmaticlabs/prysm/network/forks"
	pb "github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1"
	"github.com/prysmaticlabs/prysm/shared/bytesutil"
	"github.com/prysmaticlabs/prysm/shared/params"
	"github.com/prysmaticlabs/prysm/shared/testutil"
	"github.com/prysmaticlabs/prysm/shared/testutil/assert"
	"github.com/prysmaticlabs/prysm/shared/testutil/require"
	logTest "github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/protobuf/proto"
)

func TestSubscribe_ReceivesValidMessage(t *testing.T) {
	p2pService := p2ptest.NewTestP2P(t)
	r := Service{
		ctx: context.Background(),
		cfg: &Config{
			P2P:         p2pService,
			InitialSync: &mockSync.Sync{IsSyncing: false},
			Chain: &mockChain.ChainService{
				ValidatorsRoot: [32]byte{'A'},
				Genesis:        time.Now(),
			},
		},
		subHandler:   newSubTopicHandler(),
		chainStarted: abool.New(),
	}
	var err error
	p2pService.Digest, err = r.currentForkDigest()
	require.NoError(t, err)
	topic := "/eth2/%x/voluntary_exit"
	var wg sync.WaitGroup
	wg.Add(1)

	r.subscribe(topic, r.noopValidator, func(_ context.Context, msg proto.Message) error {
		m, ok := msg.(*pb.SignedVoluntaryExit)
		assert.Equal(t, true, ok, "Object is not of type *pb.SignedVoluntaryExit")
		if m.Exit == nil || m.Exit.Epoch != 55 {
			t.Errorf("Unexpected incoming message: %+v", m)
		}
		wg.Done()
		return nil
	}, p2pService.Digest)
	r.markForChainStart()

	p2pService.ReceivePubSub(topic, &pb.SignedVoluntaryExit{Exit: &pb.VoluntaryExit{Epoch: 55}, Signature: make([]byte, 96)})

	if testutil.WaitTimeout(&wg, time.Second) {
		t.Fatal("Did not receive PubSub in 1 second")
	}
}

func TestSubscribe_UnsubscribeTopic(t *testing.T) {
	p2pService := p2ptest.NewTestP2P(t)
	r := Service{
		ctx: context.Background(),
		cfg: &Config{
			P2P:         p2pService,
			InitialSync: &mockSync.Sync{IsSyncing: false},
			Chain: &mockChain.ChainService{
				ValidatorsRoot: [32]byte{'A'},
				Genesis:        time.Now(),
			},
		},
		chainStarted: abool.New(),
		subHandler:   newSubTopicHandler(),
	}
	var err error
	p2pService.Digest, err = r.currentForkDigest()
	require.NoError(t, err)
	topic := "/eth2/%x/voluntary_exit"

	r.subscribe(topic, r.noopValidator, func(_ context.Context, msg proto.Message) error {
		return nil
	}, p2pService.Digest)
	r.markForChainStart()

	fullTopic := fmt.Sprintf(topic, p2pService.Digest) + p2pService.Encoding().ProtocolSuffix()
	assert.Equal(t, true, r.subHandler.topicExists(fullTopic))
	topics := p2pService.PubSub().GetTopics()
	assert.Equal(t, fullTopic, topics[0])

	r.unSubscribeFromTopic(fullTopic)

	assert.Equal(t, false, r.subHandler.topicExists(fullTopic))
	assert.Equal(t, 0, len(p2pService.PubSub().GetTopics()))

}

func TestSubscribe_ReceivesAttesterSlashing(t *testing.T) {
	p2pService := p2ptest.NewTestP2P(t)
	ctx := context.Background()
	d := db.SetupDB(t)
	chainService := &mockChain.ChainService{
		Genesis:        time.Now(),
		ValidatorsRoot: [32]byte{'A'},
	}
	r := Service{
		ctx: ctx,
		cfg: &Config{
			P2P:          p2pService,
			InitialSync:  &mockSync.Sync{IsSyncing: false},
			SlashingPool: slashings.NewPool(),
			Chain:        chainService,
			DB:           d,
		},
		seenAttesterSlashingCache: make(map[uint64]bool),
		chainStarted:              abool.New(),
		subHandler:                newSubTopicHandler(),
	}
	topic := "/eth2/%x/attester_slashing"
	var wg sync.WaitGroup
	wg.Add(1)
	params.SetupTestConfigCleanup(t)
	params.OverrideBeaconConfig(params.MainnetConfig())
	var err error
	p2pService.Digest, err = r.currentForkDigest()
	require.NoError(t, err)
	r.subscribe(topic, r.noopValidator, func(ctx context.Context, msg proto.Message) error {
		require.NoError(t, r.attesterSlashingSubscriber(ctx, msg))
		wg.Done()
		return nil
	}, p2pService.Digest)
	beaconState, privKeys := testutil.DeterministicGenesisState(t, 64)
	chainService.State = beaconState
	r.markForChainStart()
	attesterSlashing, err := testutil.GenerateAttesterSlashingForValidator(
		beaconState,
		privKeys[1],
		1, /* validator index */
	)
	require.NoError(t, err, "Error generating attester slashing")
	err = r.cfg.DB.SaveState(ctx, beaconState, bytesutil.ToBytes32(attesterSlashing.Attestation_1.Data.BeaconBlockRoot))
	require.NoError(t, err)
	p2pService.ReceivePubSub(topic, attesterSlashing)

	if testutil.WaitTimeout(&wg, time.Second) {
		t.Fatal("Did not receive PubSub in 1 second")
	}
	as := r.cfg.SlashingPool.PendingAttesterSlashings(ctx, beaconState, false /*noLimit*/)
	assert.Equal(t, 1, len(as), "Expected attester slashing")
}

func TestSubscribe_ReceivesProposerSlashing(t *testing.T) {
	p2pService := p2ptest.NewTestP2P(t)
	ctx := context.Background()
	chainService := &mockChain.ChainService{
		ValidatorsRoot: [32]byte{'A'},
		Genesis:        time.Now(),
	}
	d := db.SetupDB(t)
	r := Service{
		ctx: ctx,
		cfg: &Config{
			P2P:          p2pService,
			InitialSync:  &mockSync.Sync{IsSyncing: false},
			SlashingPool: slashings.NewPool(),
			Chain:        chainService,
			DB:           d,
		},
		seenProposerSlashingCache: lruwrpr.New(10),
		chainStarted:              abool.New(),
		subHandler:                newSubTopicHandler(),
	}
	topic := "/eth2/%x/proposer_slashing"
	var wg sync.WaitGroup
	wg.Add(1)
	params.SetupTestConfigCleanup(t)
	params.OverrideBeaconConfig(params.MainnetConfig())
	var err error
	p2pService.Digest, err = r.currentForkDigest()
	require.NoError(t, err)
	r.subscribe(topic, r.noopValidator, func(ctx context.Context, msg proto.Message) error {
		require.NoError(t, r.proposerSlashingSubscriber(ctx, msg))
		wg.Done()
		return nil
	}, p2pService.Digest)
	beaconState, privKeys := testutil.DeterministicGenesisState(t, 64)
	chainService.State = beaconState
	r.markForChainStart()
	proposerSlashing, err := testutil.GenerateProposerSlashingForValidator(
		beaconState,
		privKeys[1],
		1, /* validator index */
	)
	require.NoError(t, err, "Error generating proposer slashing")

	p2pService.ReceivePubSub(topic, proposerSlashing)

	if testutil.WaitTimeout(&wg, time.Second) {
		t.Fatal("Did not receive PubSub in 1 second")
	}
	ps := r.cfg.SlashingPool.PendingProposerSlashings(ctx, beaconState, false /*noLimit*/)
	assert.Equal(t, 1, len(ps), "Expected proposer slashing")
}

func TestSubscribe_HandlesPanic(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	r := Service{
		ctx: context.Background(),
		cfg: &Config{
			Chain: &mockChain.ChainService{
				Genesis:        time.Now(),
				ValidatorsRoot: [32]byte{'A'},
			},
			P2P: p,
		},
		subHandler:   newSubTopicHandler(),
		chainStarted: abool.New(),
	}
	var err error
	p.Digest, err = r.currentForkDigest()
	require.NoError(t, err)

	topic := p2p.GossipTypeMapping[reflect.TypeOf(&pb.SignedVoluntaryExit{})]
	var wg sync.WaitGroup
	wg.Add(1)

	r.subscribe(topic, r.noopValidator, func(_ context.Context, msg proto.Message) error {
		defer wg.Done()
		panic("bad")
	}, p.Digest)
	r.markForChainStart()
	p.ReceivePubSub(topic, &pb.SignedVoluntaryExit{Exit: &pb.VoluntaryExit{Epoch: 55}, Signature: make([]byte, 96)})

	if testutil.WaitTimeout(&wg, time.Second) {
		t.Fatal("Did not receive PubSub in 1 second")
	}
}

func TestRevalidateSubscription_CorrectlyFormatsTopic(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	hook := logTest.NewGlobal()
	r := Service{
		ctx: context.Background(),
		cfg: &Config{
			Chain: &mockChain.ChainService{
				Genesis:        time.Now(),
				ValidatorsRoot: [32]byte{'A'},
			},
			P2P: p,
		},
		chainStarted: abool.New(),
		subHandler:   newSubTopicHandler(),
	}
	digest, err := r.currentForkDigest()
	require.NoError(t, err)
	subscriptions := make(map[uint64]*pubsub.Subscription, params.BeaconConfig().MaxCommitteesPerSlot)

	defaultTopic := "/eth2/testing/%#x/committee%d"
	// committee index 1
	fullTopic := fmt.Sprintf(defaultTopic, digest, 1) + r.cfg.P2P.Encoding().ProtocolSuffix()
	require.NoError(t, r.cfg.P2P.PubSub().RegisterTopicValidator(fullTopic, r.noopValidator))
	subscriptions[1], err = r.cfg.P2P.SubscribeToTopic(fullTopic)
	require.NoError(t, err)

	// committee index 2
	fullTopic = fmt.Sprintf(defaultTopic, digest, 2) + r.cfg.P2P.Encoding().ProtocolSuffix()
	err = r.cfg.P2P.PubSub().RegisterTopicValidator(fullTopic, r.noopValidator)
	require.NoError(t, err)
	subscriptions[2], err = r.cfg.P2P.SubscribeToTopic(fullTopic)
	require.NoError(t, err)

	r.reValidateSubscriptions(subscriptions, []uint64{2}, defaultTopic, digest)
	require.LogsDoNotContain(t, hook, "Could not unregister topic validator")
}

func TestStaticSubnets(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := Service{
		ctx: ctx,
		cfg: &Config{
			Chain: &mockChain.ChainService{
				Genesis:        time.Now(),
				ValidatorsRoot: [32]byte{'A'},
			},
			P2P: p,
		},
		chainStarted: abool.New(),
		subHandler:   newSubTopicHandler(),
	}
	defaultTopic := "/eth2/%x/beacon_attestation_%d"
	d, err := r.currentForkDigest()
	assert.NoError(t, err)
	r.subscribeStaticWithSubnets(defaultTopic, r.noopValidator, func(_ context.Context, msg proto.Message) error {
		// no-op
		return nil
	}, d)
	topics := r.cfg.P2P.PubSub().GetTopics()
	if uint64(len(topics)) != params.BeaconNetworkConfig().AttestationSubnetCount {
		t.Errorf("Wanted the number of subnet topics registered to be %d but got %d", params.BeaconNetworkConfig().AttestationSubnetCount, len(topics))
	}
	cancel()
}

func Test_wrapAndReportValidation(t *testing.T) {
	mChain := &mockChain.ChainService{
		Genesis:        time.Now(),
		ValidatorsRoot: [32]byte{0x01},
	}
	fd, err := forks.CreateForkDigest(mChain.GenesisTime(), mChain.ValidatorsRoot[:])
	assert.NoError(t, err)
	mockTopic := fmt.Sprintf(p2p.BlockSubnetTopicFormat, fd) + encoder.SszNetworkEncoder{}.ProtocolSuffix()
	type args struct {
		topic        string
		v            pubsub.ValidatorEx
		chainstarted bool
		pid          peer.ID
		msg          *pubsub.Message
	}
	tests := []struct {
		name string
		args args
		want pubsub.ValidationResult
	}{
		{
			name: "validator Before chainstart",
			args: args{
				topic: "foo",
				v: func(ctx context.Context, id peer.ID, message *pubsub.Message) pubsub.ValidationResult {
					return pubsub.ValidationAccept
				},
				msg: &pubsub.Message{
					Message: &pubsubpb.Message{
						Topic: func() *string {
							s := "foo"
							return &s
						}(),
					},
				},
				chainstarted: false,
			},
			want: pubsub.ValidationIgnore,
		},
		{
			name: "validator panicked",
			args: args{
				topic: "foo",
				v: func(ctx context.Context, id peer.ID, message *pubsub.Message) pubsub.ValidationResult {
					panic("oh no!")
				},
				chainstarted: true,
				msg: &pubsub.Message{
					Message: &pubsubpb.Message{
						Topic: func() *string {
							s := "foo"
							return &s
						}(),
					},
				},
			},
			want: pubsub.ValidationIgnore,
		},
		{
			name: "validator OK",
			args: args{
				topic: mockTopic,
				v: func(ctx context.Context, id peer.ID, message *pubsub.Message) pubsub.ValidationResult {
					return pubsub.ValidationAccept
				},
				chainstarted: true,
				msg: &pubsub.Message{
					Message: &pubsubpb.Message{
						Topic: func() *string {
							s := mockTopic
							return &s
						}(),
					},
				},
			},
			want: pubsub.ValidationAccept,
		},
		{
			name: "nil topic",
			args: args{
				topic: "foo",
				v: func(ctx context.Context, id peer.ID, message *pubsub.Message) pubsub.ValidationResult {
					return pubsub.ValidationAccept
				},
				msg: &pubsub.Message{
					Message: &pubsubpb.Message{
						Topic: nil,
					},
				},
			},
			want: pubsub.ValidationReject,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chainStarted := abool.New()
			chainStarted.SetTo(tt.args.chainstarted)
			s := &Service{
				chainStarted: chainStarted,
				cfg: &Config{
					Chain: mChain,
				},
				subHandler: newSubTopicHandler(),
			}
			_, v := s.wrapAndReportValidation(tt.args.topic, tt.args.v)
			got := v(context.Background(), tt.args.pid, tt.args.msg)
			if got != tt.want {
				t.Errorf("wrapAndReportValidation() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterSubnetPeers(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	ctx, cancel := context.WithCancel(context.Background())
	currSlot := types.Slot(100)
	r := Service{
		ctx: ctx,
		cfg: &Config{
			Chain: &mockChain.ChainService{
				Genesis:        time.Now(),
				ValidatorsRoot: [32]byte{'A'},
				Slot:           &currSlot,
			},
			P2P: p,
		},
		chainStarted: abool.New(),
		subHandler:   newSubTopicHandler(),
	}
	// Empty cache at the end of the test.
	defer cache.SubnetIDs.EmptyAllCaches()
	digest, err := r.currentForkDigest()
	assert.NoError(t, err)
	defaultTopic := "/eth2/%x/beacon_attestation_%d" + r.cfg.P2P.Encoding().ProtocolSuffix()
	subnet10 := r.addDigestAndIndexToTopic(defaultTopic, digest, 10)
	cache.SubnetIDs.AddAggregatorSubnetID(currSlot, 10)

	subnet20 := r.addDigestAndIndexToTopic(defaultTopic, digest, 20)
	cache.SubnetIDs.AddAttesterSubnetID(currSlot, 20)

	p1 := createPeer(t, subnet10)
	p2 := createPeer(t, subnet10, subnet20)
	p3 := createPeer(t)

	// Connect to all
	// peers.
	p.Connect(p1)
	p.Connect(p2)
	p.Connect(p3)

	// Sleep a while to allow peers to connect.
	time.Sleep(100 * time.Millisecond)

	wantedPeers := []peer.ID{p1.PeerID(), p2.PeerID(), p3.PeerID()}
	// Expect Peer 3 to be marked as suitable.
	recPeers := r.filterNeededPeers(wantedPeers)
	assert.DeepEqual(t, []peer.ID{p3.PeerID()}, recPeers)

	// Try with only peers from subnet 20.
	wantedPeers = []peer.ID{p2.BHost.ID()}
	// Connect an excess amount of peers in the particular subnet.
	for i := uint64(1); i <= params.BeaconNetworkConfig().MinimumPeersInSubnet; i++ {
		nPeer := createPeer(t, subnet20)
		p.Connect(nPeer)
		wantedPeers = append(wantedPeers, nPeer.BHost.ID())
		time.Sleep(100 * time.Millisecond)
	}

	recPeers = r.filterNeededPeers(wantedPeers)
	assert.DeepEqual(t, 1, len(recPeers), "expected at least 1 suitable peer to prune")

	cancel()
}

func TestSubscribeWithSyncSubnets_StaticOK(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	ctx, cancel := context.WithCancel(context.Background())
	currSlot := types.Slot(100)
	r := Service{
		ctx: ctx,
		cfg: &Config{
			Chain: &mockChain.ChainService{
				Genesis:        time.Now(),
				ValidatorsRoot: [32]byte{'A'},
				Slot:           &currSlot,
			},
			P2P: p,
		},
		chainStarted: abool.New(),
		subHandler:   newSubTopicHandler(),
	}
	// Empty cache at the end of the test.
	defer cache.SyncSubnetIDs.EmptyAllCaches()
	digest, err := r.currentForkDigest()
	assert.NoError(t, err)
	r.subscribeStaticWithSyncSubnets(p2p.SyncCommitteeSubnetTopicFormat, nil, nil, digest)
	assert.Equal(t, int(params.BeaconConfig().SyncCommitteeSubnetCount), len(r.cfg.P2P.PubSub().GetTopics()))
	cancel()
}

func TestSubscribeWithSyncSubnets_DynamicOK(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	params.SetupTestConfigCleanup(t)
	cfg := params.BeaconConfig()
	cfg.SecondsPerSlot = 1
	params.OverrideBeaconConfig(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	currSlot := types.Slot(100)
	r := Service{
		ctx: ctx,
		cfg: &Config{
			Chain: &mockChain.ChainService{
				Genesis:        time.Now(),
				ValidatorsRoot: [32]byte{'A'},
				Slot:           &currSlot,
			},
			P2P: p,
		},
		chainStarted: abool.New(),
		subHandler:   newSubTopicHandler(),
	}
	// Empty cache at the end of the test.
	defer cache.SyncSubnetIDs.EmptyAllCaches()
	slot := r.cfg.Chain.CurrentSlot()
	currEpoch := core.SlotToEpoch(slot)
	cache.SyncSubnetIDs.AddSyncCommitteeSubnets([]byte("pubkey"), currEpoch, []uint64{0, 1}, 10*time.Second)
	digest, err := r.currentForkDigest()
	assert.NoError(t, err)
	r.subscribeDynamicWithSyncSubnets(p2p.SyncCommitteeSubnetTopicFormat, nil, nil, digest)
	time.Sleep(2 * time.Second)
	assert.Equal(t, 2, len(r.cfg.P2P.PubSub().GetTopics()))
	topicMap := map[string]bool{}
	for _, t := range r.cfg.P2P.PubSub().GetTopics() {
		topicMap[t] = true
	}
	firstSub := fmt.Sprintf(p2p.SyncCommitteeSubnetTopicFormat, digest, 0) + r.cfg.P2P.Encoding().ProtocolSuffix()
	assert.Equal(t, true, topicMap[firstSub])

	secondSub := fmt.Sprintf(p2p.SyncCommitteeSubnetTopicFormat, digest, 1) + r.cfg.P2P.Encoding().ProtocolSuffix()
	assert.Equal(t, true, topicMap[secondSub])
	cancel()
}

func TestSubscribeWithSyncSubnets_StaticSwitchFork(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	params.SetupTestConfigCleanup(t)
	cfg := params.BeaconConfig()
	cfg.AltairForkEpoch = 1
	cfg.SecondsPerSlot = 1
	params.OverrideBeaconConfig(cfg)
	params.BeaconConfig().InitializeForkSchedule()
	ctx, cancel := context.WithCancel(context.Background())
	currSlot := types.Slot(100)
	r := Service{
		ctx: ctx,
		cfg: &Config{
			Chain: &mockChain.ChainService{
				Genesis:        time.Now().Add(-time.Duration(uint64(params.BeaconConfig().SlotsPerEpoch)*params.BeaconConfig().SecondsPerSlot) * time.Second),
				ValidatorsRoot: [32]byte{'A'},
				Slot:           &currSlot,
			},
			P2P: p,
		},
		chainStarted: abool.New(),
		subHandler:   newSubTopicHandler(),
	}
	// Empty cache at the end of the test.
	defer cache.SyncSubnetIDs.EmptyAllCaches()
	genRoot := r.cfg.Chain.GenesisValidatorRoot()
	digest, err := helpers.ComputeForkDigest(params.BeaconConfig().GenesisForkVersion, genRoot[:])
	assert.NoError(t, err)
	r.subscribeStaticWithSyncSubnets(p2p.SyncCommitteeSubnetTopicFormat, nil, nil, digest)
	assert.Equal(t, int(params.BeaconConfig().SyncCommitteeSubnetCount), len(r.cfg.P2P.PubSub().GetTopics()))

	// Expect that all old topics will be unsubscribed.
	time.Sleep(2 * time.Second)
	assert.Equal(t, 0, len(r.cfg.P2P.PubSub().GetTopics()))

	cancel()
}

func TestSubscribeWithSyncSubnets_DynamicSwitchFork(t *testing.T) {
	p := p2ptest.NewTestP2P(t)
	params.SetupTestConfigCleanup(t)
	cfg := params.BeaconConfig()
	cfg.AltairForkEpoch = 1
	cfg.SecondsPerSlot = 1
	cfg.SlotsPerEpoch = 4
	params.OverrideBeaconConfig(cfg)
	params.BeaconConfig().InitializeForkSchedule()
	ctx, cancel := context.WithCancel(context.Background())
	currSlot := types.Slot(100)
	r := Service{
		ctx: ctx,
		cfg: &Config{
			Chain: &mockChain.ChainService{
				Genesis:        time.Now().Add(-time.Duration(params.BeaconConfig().SecondsPerSlot) * time.Second),
				ValidatorsRoot: [32]byte{'A'},
				Slot:           &currSlot,
			},
			P2P: p,
		},
		chainStarted: abool.New(),
		subHandler:   newSubTopicHandler(),
	}
	// Empty cache at the end of the test.
	defer cache.SyncSubnetIDs.EmptyAllCaches()
	cache.SyncSubnetIDs.AddSyncCommitteeSubnets([]byte("pubkey"), 0, []uint64{0, 1}, 10*time.Second)
	genRoot := r.cfg.Chain.GenesisValidatorRoot()
	digest, err := helpers.ComputeForkDigest(params.BeaconConfig().GenesisForkVersion, genRoot[:])
	assert.NoError(t, err)

	r.subscribeDynamicWithSyncSubnets(p2p.SyncCommitteeSubnetTopicFormat, nil, nil, digest)
	time.Sleep(2 * time.Second)
	assert.Equal(t, 2, len(r.cfg.P2P.PubSub().GetTopics()))
	topicMap := map[string]bool{}
	for _, t := range r.cfg.P2P.PubSub().GetTopics() {
		topicMap[t] = true
	}
	firstSub := fmt.Sprintf(p2p.SyncCommitteeSubnetTopicFormat, digest, 0) + r.cfg.P2P.Encoding().ProtocolSuffix()
	assert.Equal(t, true, topicMap[firstSub])

	secondSub := fmt.Sprintf(p2p.SyncCommitteeSubnetTopicFormat, digest, 1) + r.cfg.P2P.Encoding().ProtocolSuffix()
	assert.Equal(t, true, topicMap[secondSub])

	// Expect that all old topics will be unsubscribed.
	time.Sleep(2 * time.Second)
	assert.Equal(t, 0, len(r.cfg.P2P.PubSub().GetTopics()))

	cancel()
}

func TestIsDigestValid(t *testing.T) {
	genRoot := [32]byte{'A'}
	digest, err := helpers.ComputeForkDigest(params.BeaconConfig().GenesisForkVersion, genRoot[:])
	assert.NoError(t, err)
	valid, err := isDigestValid(digest, time.Now().Add(-100*time.Second), genRoot)
	assert.NoError(t, err)
	assert.Equal(t, true, valid)

	// Compute future fork digest that will be invalid currently.
	digest, err = helpers.ComputeForkDigest(params.BeaconConfig().AltairForkVersion, genRoot[:])
	assert.NoError(t, err)
	valid, err = isDigestValid(digest, time.Now().Add(-100*time.Second), genRoot)
	assert.NoError(t, err)
	assert.Equal(t, false, valid)
}

// Create peer and register them to provided topics.
func createPeer(t *testing.T, topics ...string) *p2ptest.TestP2P {
	p := p2ptest.NewTestP2P(t)
	for _, tp := range topics {
		jTop, err := p.PubSub().Join(tp)
		if err != nil {
			t.Fatal(err)
		}
		_, err = jTop.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
	}
	return p
}
