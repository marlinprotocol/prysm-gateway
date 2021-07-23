package kv

import (
	"context"

	statepb "github.com/prysmaticlabs/prysm/proto/prysm/v2/state"
	"github.com/prysmaticlabs/prysm/shared/bytesutil"
	bolt "go.etcd.io/bbolt"
	"go.opencensus.io/trace"
)

// SaveStateSummary saves a state summary object to the DB.
func (s *Store) SaveStateSummary(ctx context.Context, summary *statepb.StateSummary) error {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.SaveStateSummary")
	defer span.End()

	return s.SaveStateSummaries(ctx, []*statepb.StateSummary{summary})
}

// SaveStateSummaries saves state summary objects to the DB.
func (s *Store) SaveStateSummaries(ctx context.Context, summaries []*statepb.StateSummary) error {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.SaveStateSummaries")
	defer span.End()

	// When we reach the state summary cache prune count,
	// dump the cached state summaries to the DB.
	if s.stateSummaryCache.len() >= stateSummaryCachePruneCount {
		if err := s.saveCachedStateSummariesDB(ctx); err != nil {
			return err
		}
	}

	for _, ss := range summaries {
		s.stateSummaryCache.put(bytesutil.ToBytes32(ss.Root), ss)
	}

	return nil
}

// StateSummary returns the state summary object from the db using input block root.
func (s *Store) StateSummary(ctx context.Context, blockRoot [32]byte) (*statepb.StateSummary, error) {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.StateSummary")
	defer span.End()

	if s.stateSummaryCache.has(blockRoot) {
		return s.stateSummaryCache.get(blockRoot), nil
	}

	enc, err := s.stateSummaryBytes(ctx, blockRoot)
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		return nil, nil
	}
	summary := &statepb.StateSummary{}
	if err := decode(ctx, enc, summary); err != nil {
		return nil, err
	}
	return summary, nil
}

// HasStateSummary returns true if a state summary exists in DB.
func (s *Store) HasStateSummary(ctx context.Context, blockRoot [32]byte) bool {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.HasStateSummary")
	defer span.End()

	if s.stateSummaryCache.has(blockRoot) {
		return true
	}

	enc, err := s.stateSummaryBytes(ctx, blockRoot)
	if err != nil {
		panic(err)
	}
	return len(enc) > 0
}

func (s *Store) stateSummaryBytes(ctx context.Context, blockRoot [32]byte) ([]byte, error) {
	ctx, span := trace.StartSpan(ctx, "BeaconDB.stateSummaryBytes")
	defer span.End()

	var enc []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(stateSummaryBucket)
		enc = bucket.Get(blockRoot[:])
		return nil
	})

	return enc, err
}

// This saves all cached state summary objects to DB, and clears up the cache.
func (s *Store) saveCachedStateSummariesDB(ctx context.Context) error {
	summaries := s.stateSummaryCache.getAll()
	encs := make([][]byte, len(summaries))
	for i, s := range summaries {
		enc, err := encode(ctx, s)
		if err != nil {
			return err
		}
		encs[i] = enc
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(stateSummaryBucket)
		for i, s := range summaries {
			if err := bucket.Put(s.Root, encs[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.stateSummaryCache.clear()
	return nil
}
