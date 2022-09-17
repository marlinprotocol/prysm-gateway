package sync

import (
	"encoding/binary"
	"net"
	"time"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

func (s *Service) marlinService(endpoint string, msgChan chan *pubsub.Message) {
	var backoffDuration time.Duration = 1
	log.Info("Starting Marlin service")
	defer log.Info("Shutting down Marlin service")
	for {
		// try to connect
		conn, err := net.Dial("tcp", endpoint)

		if err != nil {
			log.WithError(err).Error("Could not connect to Marlin endpoint")
			// wait for backoff or cancellation
			select {
			case <-s.ctx.Done():
				// nothing to do, return
				return
			case <-time.NewTimer(backoffDuration * time.Second).C:
				// increase backoff and retry
				backoffDuration *= 2
				if(backoffDuration > 64) {
					backoffDuration = 64
				}
				continue
			}
		}

		// reset backoff
		backoffDuration = 1

		defer func () {
			err := conn.Close()
			if(err != nil) {
				log.WithError(err).Error("Conn close error")
			}
		}()

		rc := make(chan int)

		// read loop
		go func() {
			buf := make([]byte, 10000000)  // 10 MB
			var size uint64 = 0
			var waitFor uint64 = 0
			for {
				// read
				n, err := conn.Read(buf[size:])
				if err != nil {
					// abort and return
					log.WithError(err).Error("Could not read from conn")
					close(rc)
					return
				}
				size += uint64(n)

				// check if length is available
				if(size < 8) {
					continue
				}

				// check if length has not been set
				if(waitFor == 0) {
					waitFor = binary.BigEndian.Uint64(buf[0:8]) + 8
				}

				// check if full message is available
				if(size < waitFor) {
					continue
				}

				// extract topic
				topicSize := binary.BigEndian.Uint64(buf[8:16])
				if(16 + topicSize > size) {
					// too big, drop msg
					continue
				}
				topic := string(buf[16:16+topicSize])

				// copy slice and publish message
				msg := make([]byte, size - 16 - topicSize)
				copy(msg, buf[16 + topicSize:size])
				err = s.cfg.p2p.PublishToTopic(s.ctx, topic, msg)
				if(err != nil) {
					log.WithError(err).Error("Could not publish to topic")
				}

				// reset state
				size = 0
				waitFor = 0
			}
		}()

		// main service loop
		service:
		for {
			select {
			case <-s.ctx.Done():
				// nothing to do, return
				return
			case <-rc:
				// read failed, likely conn error, retry with new conn
				break service
			case msg := <-msgChan:
				log.Info("Block msg from node: ", len(msg.Data))
				msgData := msg.Data
				topic := *msg.Topic
				buf := make([]byte, len(msgData) + len(topic) + 16)
				binary.BigEndian.PutUint64(buf[0:8], uint64(len(msgData) + len(topic) + 8))
				binary.BigEndian.PutUint64(buf[8:16], uint64(len(topic)))
				copy(buf[16:], topic)
				copy(buf[16+len(topic):], msgData)
				// log.Info("Buf: ", buf, len(buf))
				written := 0
				for written < len(buf) {
					n, err := conn.Write(buf[written:])
					if err != nil {
						// write failed, likely conn error, retry with new conn
						log.WithError(err).Error("Could not write on conn")
						break service
					}
					written += n
				}
			}
		}
		err = conn.Close()
		if(err != nil) {
			log.WithError(err).Error("Conn close error")
		}
	}
}

