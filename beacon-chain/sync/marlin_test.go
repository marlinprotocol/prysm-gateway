package sync

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p"
	p2ptest "github.com/OffchainLabs/prysm/v7/beacon-chain/p2p/testing"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/startup"
	"github.com/OffchainLabs/prysm/v7/config/params"
	"github.com/OffchainLabs/prysm/v7/testing/assert"
	"github.com/OffchainLabs/prysm/v7/testing/require"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
)

// marlinHeader builds a frame header with the given raw lengths.
func marlinHeader(length, topicSize uint64) []byte {
	hdr := make([]byte, marlinHeaderSize)
	binary.BigEndian.PutUint64(hdr[0:8], length)
	binary.BigEndian.PutUint64(hdr[8:16], topicSize)
	return hdr
}

func TestParseMarlinFrameHeader(t *testing.T) {
	tests := []struct {
		name      string
		hdr       []byte
		topicSize int
		msgSize   int
		err       error
	}{
		{name: "topic and message", hdr: marlinHeader(8+5+100, 5), topicSize: 5, msgSize: 100},
		{name: "empty topic and message", hdr: marlinHeader(8, 0), topicSize: 0, msgSize: 0},
		{name: "empty message", hdr: marlinHeader(8+5, 5), topicSize: 5, msgSize: 0},
		{name: "empty topic", hdr: marlinHeader(8+7, 0), topicSize: 0, msgSize: 7},
		{name: "longest topic", hdr: marlinHeader(8+marlinMaxTopicSize, marlinMaxTopicSize), topicSize: marlinMaxTopicSize, msgSize: 0},
		{name: "largest frame", hdr: marlinHeader(marlinMaxFrameSize-8, 40), topicSize: 40, msgSize: marlinMaxFrameSize - marlinHeaderSize - 40},
		{name: "extra bytes after the header are ignored", hdr: append(marlinHeader(8+1+2, 1), 1, 2, 3, 4), topicSize: 1, msgSize: 2},

		{name: "nil header", hdr: nil, err: errMarlinFrameMalformed},
		{name: "short header", hdr: marlinHeader(8, 0)[:15], err: errMarlinFrameMalformed},
		{name: "zero length", hdr: marlinHeader(0, 0), err: errMarlinFrameMalformed},
		{name: "length without room for the topic length", hdr: marlinHeader(7, 0), err: errMarlinFrameMalformed},
		{name: "one over the largest frame", hdr: marlinHeader(marlinMaxFrameSize-7, 40), err: errMarlinFrameTooLarge},
		{name: "20 MB frame", hdr: marlinHeader(20000000, 40), err: errMarlinFrameTooLarge},
		{name: "length 2^63", hdr: marlinHeader(1<<63, 40), err: errMarlinFrameTooLarge},
		{name: "length 2^64-8, +8 wraps to 0", hdr: marlinHeader(0xFFFFFFFFFFFFFFF8, 0), err: errMarlinFrameTooLarge},
		{name: "length 2^64-1", hdr: marlinHeader(0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF), err: errMarlinFrameTooLarge},
		{name: "topic one byte longer than the frame", hdr: marlinHeader(8+5, 6), err: errMarlinFrameMalformed},
		{name: "topic longer than the limit", hdr: marlinHeader(8+1000, marlinMaxTopicSize+1), err: errMarlinFrameMalformed},
		{name: "topic length 2^64-16, 16+ wraps to 0", hdr: marlinHeader(8+100, 0xFFFFFFFFFFFFFFF0), err: errMarlinFrameMalformed},
		{name: "topic length 2^64-1", hdr: marlinHeader(8+100, 0xFFFFFFFFFFFFFFFF), err: errMarlinFrameMalformed},
		{name: "topic length 2^63", hdr: marlinHeader(8+100, 1<<63), err: errMarlinFrameMalformed},
		{name: "topic length 2^32, 0 as a 32 bit int", hdr: marlinHeader(8+100, 1<<32), err: errMarlinFrameMalformed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topicSize, msgSize, err := parseMarlinFrameHeader(tt.hdr)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.topicSize, topicSize)
			assert.Equal(t, tt.msgSize, msgSize)
		})
	}
}

type marlinTestFrame struct {
	topic string
	msg   []byte
}

func TestReadMarlinFrames(t *testing.T) {
	blockTopic := "/eth2/abcdef01/beacon_block/ssz_snappy"
	f1 := marlinTestFrame{blockTopic, []byte("first block")}
	f2 := marlinTestFrame{blockTopic, bytes.Repeat([]byte{0xab}, 100000)}
	f3 := marlinTestFrame{"", []byte{}}
	f4 := marlinTestFrame{"t", []byte{0}}
	// The message of f5 looks like a header with a huge length
	f5 := marlinTestFrame{blockTopic, marlinHeader(0xFFFFFFFFFFFFFFF0, 0xFFFFFFFFFFFFFFF0)}
	enc := func(frames ...marlinTestFrame) []byte {
		var buf []byte
		for _, f := range frames {
			buf = append(buf, encodeMarlinFrame(f.topic, f.msg)...)
		}
		return buf
	}

	tests := []struct {
		name   string
		input  []byte
		frames []marlinTestFrame
		// error which ends the stream after the frames
		err error
	}{
		{name: "no data", input: nil, err: io.EOF},
		{name: "one frame", input: enc(f1), frames: []marlinTestFrame{f1}, err: io.EOF},
		{name: "frames arriving together are not merged", input: enc(f1, f2, f3, f4, f5, f1), frames: []marlinTestFrame{f1, f2, f3, f4, f5, f1}, err: io.EOF},
		{name: "stream ends inside a header", input: append(enc(f1), enc(f2)[:10]...), frames: []marlinTestFrame{f1}, err: io.ErrUnexpectedEOF},
		{name: "stream ends inside a topic", input: append(enc(f1), enc(f2)[:20]...), frames: []marlinTestFrame{f1}, err: io.ErrUnexpectedEOF},
		{name: "stream ends inside a message", input: enc(f1, f2)[:len(enc(f1, f2))-1], frames: []marlinTestFrame{f1}, err: io.ErrUnexpectedEOF},
		{name: "stream ends after a header", input: enc(f2)[:marlinHeaderSize], err: io.ErrUnexpectedEOF},
		{name: "topic length which overflows", input: append(enc(f1), append(marlinHeader(8+100, 0xFFFFFFFFFFFFFFF0), make([]byte, 100)...)...), frames: []marlinTestFrame{f1}, err: errMarlinFrameMalformed},
		{name: "frame larger than the limit", input: append(enc(f1, f4), append(marlinHeader(20000000, 4), enc(f1)...)...), frames: []marlinTestFrame{f1, f4}, err: errMarlinFrameTooLarge},
		{name: "length which overflows", input: marlinHeader(0xFFFFFFFFFFFFFFF8, 0), err: errMarlinFrameTooLarge},
		{name: "garbage", input: bytes.Repeat([]byte{0xff}, 1000), err: errMarlinFrameTooLarge},
		{name: "zeros", input: make([]byte, 1000), err: errMarlinFrameMalformed},
	}

	readers := []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{"all at once", func(r io.Reader) io.Reader { return r }},
		{"one byte per read", iotest.OneByteReader},
		{"half reads", iotest.HalfReader},
		{"data together with EOF", iotest.DataErrReader},
	}

	for _, tt := range tests {
		for _, rd := range readers {
			t.Run(tt.name+"/"+rd.name, func(t *testing.T) {
				var got []marlinTestFrame
				n, err := marlinReadLoop(rd.wrap(bytes.NewReader(tt.input)), func(topic string, msg []byte) {
					got = append(got, marlinTestFrame{topic, msg})
				})
				require.ErrorIs(t, err, tt.err)
				require.Equal(t, uint64(len(tt.frames)), n)
				require.Equal(t, len(tt.frames), len(got))
				for i := range tt.frames {
					assert.Equal(t, tt.frames[i].topic, got[i].topic)
					assert.Equal(t, true, bytes.Equal(tt.frames[i].msg, got[i].msg), "message %d differs", i)
				}
			})
		}
	}
}

func TestMarlinReadLoop_RecoversFromPanic(t *testing.T) {
	input := append(encodeMarlinFrame("a", []byte{1}), encodeMarlinFrame("b", []byte{2})...)
	calls := 0
	n, err := marlinReadLoop(bytes.NewReader(input), func(string, []byte) {
		calls++
		var s []byte
		// index out of range
		_ = s[calls]
	})
	require.ErrorContains(t, "panic in Marlin read loop", err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, uint64(1), n)
}

func TestEncodeMarlinFrame(t *testing.T) {
	buf := encodeMarlinFrame("topic", []byte{1, 2, 3})
	want := append(marlinHeader(8+5+3, 5), 't', 'o', 'p', 'i', 'c', 1, 2, 3)
	assert.DeepEqual(t, want, buf)

	topic, msg, err := readMarlinFrame(bytes.NewReader(buf))
	require.NoError(t, err)
	assert.Equal(t, "topic", topic)
	assert.DeepEqual(t, []byte{1, 2, 3}, msg)
}

func TestNextMarlinBackoff(t *testing.T) {
	want := []time.Duration{2, 4, 8, 16, 32, 64, 64, 64}
	d := marlinMinBackoff
	for i, w := range want {
		d = nextMarlinBackoff(d)
		assert.Equal(t, w*time.Second, d, "step %d", i)
	}
	assert.Equal(t, marlinMinBackoff, nextMarlinBackoff(0))
	assert.Equal(t, marlinMaxBackoff, nextMarlinBackoff(time.Hour))
}

// marlinTestP2P records what is published. With block set, PublishToTopic
// waits for its context like the real one does on a topic without peers.
type marlinTestP2P struct {
	*p2ptest.TestP2P
	block bool

	mu        sync.Mutex
	published []marlinTestFrame
	done      chan error
}

func (m *marlinTestP2P) PublishToTopic(ctx context.Context, topic string, data []byte, _ ...pubsub.PubOpt) error {
	if m.block {
		<-ctx.Done()
		m.done <- ctx.Err()
		return ctx.Err()
	}
	m.mu.Lock()
	m.published = append(m.published, marlinTestFrame{topic, data})
	m.mu.Unlock()
	m.done <- nil
	return nil
}

func newMarlinTestService(t *testing.T, block bool) (*Service, *marlinTestP2P, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := &marlinTestP2P{TestP2P: p2ptest.NewTestP2P(t), block: block, done: make(chan error, 16)}
	s := &Service{
		ctx: ctx,
		cfg: &config{p2p: m, clock: startup.NewClock(time.Now(), [32]byte{'A'})},
	}
	return s, m, cancel
}

func marlinBlockTopic(s *Service) string {
	return fmt.Sprintf(p2p.BlockSubnetTopicFormat, params.ForkDigest(s.cfg.clock.CurrentEpoch())) + s.cfg.p2p.Encoding().ProtocolSuffix()
}

func TestMarlinTopics(t *testing.T) {
	s, _, _ := newMarlinTestService(t, false)
	topics := s.marlinTopics()
	require.Equal(t, 1, len(topics))
	assert.Equal(t, marlinBlockTopic(s), topics[0])
	assert.Equal(t, true, strings.HasPrefix(topics[0], "/eth2/"))
	assert.Equal(t, true, strings.HasSuffix(topics[0], "/beacon_block/ssz_snappy"))
}

// startMarlinSession runs a session on one end of a pipe and returns the
// other end and a channel with the result of the session.
func startMarlinSession(s *Service, msgChan chan *pubsub.Message) (net.Conn, chan bool) {
	bridge, gateway := net.Pipe()
	res := make(chan bool, 1)
	go func() { res <- s.marlinSession(gateway, msgChan) }()
	return bridge, res
}

func waitMarlin[T any](t *testing.T, ch chan T, what string) T {
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func TestMarlinSession_PublishesOnlyCurrentBlockTopic(t *testing.T) {
	s, m, _ := newMarlinTestService(t, false)
	bridge, res := startMarlinSession(s, make(chan *pubsub.Message))
	topic := marlinBlockTopic(s)

	// All frames in one write: an old fork digest, a made up topic, then two
	// blocks on the current topic
	var buf []byte
	buf = append(buf, encodeMarlinFrame("/eth2/00000000/beacon_block/ssz_snappy", []byte("old fork"))...)
	buf = append(buf, encodeMarlinFrame("made up topic", []byte("x"))...)
	buf = append(buf, encodeMarlinFrame(topic, []byte("block 1"))...)
	buf = append(buf, encodeMarlinFrame(topic, []byte("block 2"))...)
	_, err := bridge.Write(buf)
	require.NoError(t, err)

	require.NoError(t, waitMarlin(t, m.done, "first publish"))
	require.NoError(t, waitMarlin(t, m.done, "second publish"))

	require.NoError(t, bridge.Close())
	assert.Equal(t, true, waitMarlin(t, res, "session end"), "session with frames is healthy")

	m.mu.Lock()
	defer m.mu.Unlock()
	require.Equal(t, 2, len(m.published))
	assert.Equal(t, topic, m.published[0].topic)
	assert.DeepEqual(t, []byte("block 1"), m.published[0].msg)
	assert.Equal(t, topic, m.published[1].topic)
	assert.DeepEqual(t, []byte("block 2"), m.published[1].msg)
}

func TestMarlinSession_MalformedFrameEndsSession(t *testing.T) {
	tests := []struct {
		name string
		hdr  []byte
	}{
		{"topic length overflow", marlinHeader(8+100, 0xFFFFFFFFFFFFFFF0)},
		{"first 8 bytes ff..f0", append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xf0}, make([]byte, 8)...)},
		{"20 MB length", marlinHeader(20000000, 4)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, m, _ := newMarlinTestService(t, false)
			bridge, res := startMarlinSession(s, make(chan *pubsub.Message))

			_, err := bridge.Write(tt.hdr)
			require.NoError(t, err)

			assert.Equal(t, false, waitMarlin(t, res, "session end"), "session without a frame is not healthy")
			// The session closed its end
			_, err = bridge.Read(make([]byte, 1))
			require.ErrorIs(t, err, io.EOF)
			assert.Equal(t, 0, len(m.published))
		})
	}
}

func TestMarlinSession_PublishTimesOut(t *testing.T) {
	old := marlinPublishTimeout
	marlinPublishTimeout = 50 * time.Millisecond
	defer func() { marlinPublishTimeout = old }()

	s, m, _ := newMarlinTestService(t, true)
	bridge, res := startMarlinSession(s, make(chan *pubsub.Message))
	topic := marlinBlockTopic(s)

	// No peer ever shows up for the first message, the second is still read
	_, err := bridge.Write(append(encodeMarlinFrame(topic, []byte("block 1")), encodeMarlinFrame(topic, []byte("block 2"))...))
	require.NoError(t, err)
	require.ErrorIs(t, waitMarlin(t, m.done, "first publish"), context.DeadlineExceeded)
	require.ErrorIs(t, waitMarlin(t, m.done, "second publish"), context.DeadlineExceeded)

	require.NoError(t, bridge.Close())
	waitMarlin(t, res, "session end")
}

func TestMarlinSession_StopsWithServiceWhilePublishing(t *testing.T) {
	old := marlinPublishTimeout
	marlinPublishTimeout = time.Hour
	defer func() { marlinPublishTimeout = old }()

	s, m, cancel := newMarlinTestService(t, true)
	bridge, res := startMarlinSession(s, make(chan *pubsub.Message))
	defer func() { _ = bridge.Close() }()

	go func() {
		_, _ = bridge.Write(encodeMarlinFrame(marlinBlockTopic(s), []byte("block")))
	}()
	// Give the reader time to block in PublishToTopic
	time.Sleep(50 * time.Millisecond)
	cancel()

	require.ErrorIs(t, waitMarlin(t, m.done, "publish"), context.Canceled)
	// marlinSession waited for its reader
	waitMarlin(t, res, "session end")
}

func TestMarlinSession_SendsBlocksToBridge(t *testing.T) {
	s, _, _ := newMarlinTestService(t, false)
	msgChan := make(chan *pubsub.Message)
	bridge, res := startMarlinSession(s, msgChan)

	topic := marlinBlockTopic(s)
	msg := &pubsub.Message{Message: &pubsubpb.Message{Data: []byte("block from node"), Topic: &topic}}
	go func() { msgChan <- msg }()

	gotTopic, gotMsg, err := readMarlinFrame(bridge)
	require.NoError(t, err)
	assert.Equal(t, topic, gotTopic)
	assert.DeepEqual(t, []byte("block from node"), gotMsg)

	require.NoError(t, bridge.Close())
	assert.Equal(t, true, waitMarlin(t, res, "session end"))
}

func TestMarlinService_BacksOffAndReconnects(t *testing.T) {
	// A bridge which accepts and closes every connection at once
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	accepted := make(chan time.Time, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- time.Now()
			_ = conn.Close()
		}
	}()

	s, _, cancel := newMarlinTestService(t, false)
	stopped := make(chan struct{})
	go func() {
		s.marlinService(ln.Addr().String(), make(chan *pubsub.Message))
		close(stopped)
	}()

	first := waitMarlin(t, accepted, "first connection")
	second := waitMarlin(t, accepted, "second connection")
	third := waitMarlin(t, accepted, "third connection")
	// 1 s, then 2 s: no tight reconnect loop
	assert.Equal(t, true, second.Sub(first) >= 900*time.Millisecond, "first backoff %s", second.Sub(first))
	assert.Equal(t, true, third.Sub(second) >= 1900*time.Millisecond, "second backoff %s", third.Sub(second))

	cancel()
	waitMarlin(t, stopped, "service stop")
}
