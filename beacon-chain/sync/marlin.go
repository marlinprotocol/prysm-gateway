package sync

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"slices"
	"time"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p"
	"github.com/OffchainLabs/prysm/v7/config/params"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/pkg/errors"
)

// Frames exchanged with the Marlin bridge:
//
//	8 bytes   big endian length of the rest of the frame (8 + topic + message)
//	8 bytes   big endian length of the topic
//	topic
//	message
const (
	marlinHeaderSize = 16
	// Largest frame accepted from the bridge, header included. The buffer of
	// the old read loop had the same size.
	marlinMaxFrameSize = 10 * 1000 * 1000
	// Gossip topics are short, e.g. /eth2/abcdef01/beacon_block/ssz_snappy.
	marlinMaxTopicSize = 256
	// Longest time to write one frame to the bridge.
	marlinWriteTimeout = 10 * time.Second
	// Backoff between connection attempts.
	marlinMinBackoff = time.Second
	marlinMaxBackoff = 64 * time.Second
)

// Longest wait for a peer on the topic when publishing a message.
var marlinPublishTimeout = 2 * time.Second

var (
	// errMarlinFrameTooLarge and errMarlinFrameMalformed mean the stream can
	// not be trusted any more, the connection has to be dropped.
	errMarlinFrameTooLarge  = errors.New("marlin frame is too large")
	errMarlinFrameMalformed = errors.New("marlin frame is malformed")
)

// parseMarlinFrameHeader checks the two lengths at the start of a frame and
// returns the size of the topic and of the message that follow the header.
// All comparisons are done before any arithmetic on the lengths, so no value
// read from the network can overflow.
func parseMarlinFrameHeader(hdr []byte) (topicSize, msgSize int, err error) {
	if len(hdr) < marlinHeaderSize {
		return 0, 0, errors.Wrap(errMarlinFrameMalformed, "short header")
	}
	length := binary.BigEndian.Uint64(hdr[0:8])
	topicLen := binary.BigEndian.Uint64(hdr[8:16])

	// length counts the topic length field, the topic and the message
	if length < 8 {
		return 0, 0, errors.Wrapf(errMarlinFrameMalformed, "length %d", length)
	}
	if length > marlinMaxFrameSize-8 {
		return 0, 0, errors.Wrapf(errMarlinFrameTooLarge, "length %d", length)
	}
	rest := length - 8
	if topicLen > marlinMaxTopicSize {
		return 0, 0, errors.Wrapf(errMarlinFrameMalformed, "topic length %d", topicLen)
	}
	if topicLen > rest {
		return 0, 0, errors.Wrapf(errMarlinFrameMalformed, "topic length %d in a frame of length %d", topicLen, length)
	}
	return int(topicLen), int(rest - topicLen), nil
}

// readMarlinFrame reads exactly one frame from r. It returns io.EOF only when
// r ends before the first byte of a frame.
func readMarlinFrame(r io.Reader) (topic string, msg []byte, err error) {
	var hdr [marlinHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", nil, err
	}
	topicSize, msgSize, err := parseMarlinFrameHeader(hdr[:])
	if err != nil {
		return "", nil, err
	}
	body := make([]byte, topicSize+msgSize)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return "", nil, err
	}
	return string(body[:topicSize]), body[topicSize:], nil
}

// encodeMarlinFrame builds the frame for a message on a topic.
func encodeMarlinFrame(topic string, msg []byte) []byte {
	buf := make([]byte, marlinHeaderSize+len(topic)+len(msg))
	binary.BigEndian.PutUint64(buf[0:8], uint64(len(msg)+len(topic)+8))
	binary.BigEndian.PutUint64(buf[8:16], uint64(len(topic)))
	copy(buf[marlinHeaderSize:], topic)
	copy(buf[marlinHeaderSize+len(topic):], msg)
	return buf
}

// nextMarlinBackoff doubles the backoff up to marlinMaxBackoff.
func nextMarlinBackoff(d time.Duration) time.Duration {
	if d < marlinMinBackoff {
		return marlinMinBackoff
	}
	if d >= marlinMaxBackoff/2 {
		return marlinMaxBackoff
	}
	return d * 2
}

// marlinTopics returns the topics on which messages from the bridge are
// published: the beacon block topic of the current fork, and of the next
// fork when it starts in the next epoch. The topic of a frame comes from
// the network, publishing to any other would join a topic without peers.
func (s *Service) marlinTopics() []string {
	suffix := s.cfg.p2p.Encoding().ProtocolSuffix()
	current := s.cfg.clock.CurrentEpoch()
	topics := []string{fmt.Sprintf(p2p.BlockSubnetTopicFormat, params.ForkDigest(current)) + suffix}
	if next := params.NextNetworkScheduleEntry(current); next.Epoch == current+1 {
		topics = append(topics, fmt.Sprintf(p2p.BlockSubnetTopicFormat, next.ForkDigest)+suffix)
	}
	return topics
}

// marlinPublish publishes a message received from the bridge. It gives up
// after marlinPublishTimeout, PublishToTopic waits for a peer on the topic.
func (s *Service) marlinPublish(ctx context.Context, topic string, msg []byte) {
	if !slices.Contains(s.marlinTopics(), topic) {
		log.WithField("topic", topic).Debug("Dropped Marlin message on a topic which is not the current beacon block topic")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, marlinPublishTimeout)
	defer cancel()
	if err := s.cfg.p2p.PublishToTopic(ctx, topic, msg); err != nil {
		log.WithError(err).Error("Could not publish to topic")
	}
}

// marlinReadLoop reads frames from r and hands them to publish until r
// fails or a frame is malformed. It returns the number of frames read and
// the error that ended it, and never panics.
func marlinReadLoop(r io.Reader, publish func(topic string, msg []byte)) (frames uint64, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errors.Errorf("panic in Marlin read loop: %v", p)
		}
	}()
	for {
		topic, msg, err := readMarlinFrame(r)
		if err != nil {
			return frames, err
		}
		frames++
		publish(topic, msg)
	}
}

// marlinSession exchanges messages with the bridge over conn until the
// connection fails or the service stops. It closes conn and waits for the
// reader before returning. healthy is true when at least one frame was read
// or written.
func (s *Service) marlinSession(conn net.Conn, msgChan chan *pubsub.Message) (healthy bool) {
	ctx, cancel := context.WithCancel(s.ctx)

	type readResult struct {
		frames uint64
		err    error
	}
	rc := make(chan readResult, 1)
	go func() {
		frames, err := marlinReadLoop(conn, func(topic string, msg []byte) {
			s.marlinPublish(ctx, topic, msg)
		})
		rc <- readResult{frames, err}
	}()

	readDone := false
	defer func() {
		// Aborts a publish in progress and unblocks the pending read
		cancel()
		if err := conn.Close(); err != nil {
			log.WithError(err).Error("Conn close error")
		}
		if !readDone {
			res := <-rc
			healthy = healthy || res.frames > 0
		}
	}()

	for {
		select {
		case <-s.ctx.Done():
			return healthy
		case res := <-rc:
			// read failed, likely conn error, retry with new conn
			readDone = true
			log.WithError(res.err).Error("Could not read from conn")
			return healthy || res.frames > 0
		case msg := <-msgChan:
			if msg == nil || msg.Topic == nil {
				continue
			}
			log.Info("Block msg from node: ", len(msg.Data))
			buf := encodeMarlinFrame(*msg.Topic, msg.Data)
			// a bridge which stopped reading must not block the service
			if err := conn.SetWriteDeadline(time.Now().Add(marlinWriteTimeout)); err != nil {
				log.WithError(err).Error("Could not set write deadline on conn")
				return healthy
			}
			written := 0
			for written < len(buf) {
				n, err := conn.Write(buf[written:])
				if err != nil {
					// write failed, likely conn error, retry with new conn
					log.WithError(err).Error("Could not write on conn")
					return healthy
				}
				written += n
			}
			healthy = true
		}
	}
}

func (s *Service) marlinService(endpoint string, msgChan chan *pubsub.Message) {
	backoff := marlinMinBackoff
	log.Info("Starting Marlin service")
	defer log.Info("Shutting down Marlin service")
	for {
		// try to connect
		dialer := net.Dialer{Timeout: 10 * time.Second}
		conn, err := dialer.DialContext(s.ctx, "tcp", endpoint)
		if err != nil {
			log.WithError(err).Error("Could not connect to Marlin endpoint")
		} else if s.marlinSession(conn, msgChan) {
			// the connection worked, start over with the backoff
			backoff = marlinMinBackoff
		}

		// wait for backoff or cancellation, also after a connection which
		// failed, so that a peer which accepts and then fails is not retried
		// in a tight loop
		timer := time.NewTimer(backoff)
		select {
		case <-s.ctx.Done():
			// nothing to do, return
			timer.Stop()
			return
		case <-timer.C:
			backoff = nextMarlinBackoff(backoff)
		}
	}
}
