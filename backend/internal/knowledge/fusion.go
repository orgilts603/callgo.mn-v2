package knowledge

import (
	"sort"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// rrfK is the Reciprocal Rank Fusion constant (Cormack et al. 2009).
const rrfK = 60

// fuseRRF merges ranked hit lists with Reciprocal Rank Fusion: a chunk
// scores Σ 1/(rrfK + rank) over the lists it appears in (rank from 1). Scores
// are normalised by the best possible total (rank 1 in every list), so 1.0
// means "first everywhere" and every score lies in [0,1]. Chunks are
// deduplicated; ties keep first-seen order. At most k hits are returned
// (all when k <= 0).
func fuseRRF(k int, lists ...[]domain.KnowledgeHit) []domain.KnowledgeHit {
	type entry struct {
		hit   domain.KnowledgeHit
		score float64
		order int
	}
	byID := map[uuid.UUID]*entry{}
	var entries []*entry
	for _, list := range lists {
		seen := map[uuid.UUID]bool{}
		rank := 0
		for _, h := range list {
			if seen[h.ChunkID] {
				continue // a list repeating a chunk counts it once, at its best rank
			}
			seen[h.ChunkID] = true
			rank++
			e, ok := byID[h.ChunkID]
			if !ok {
				e = &entry{hit: h, order: len(entries)}
				byID[h.ChunkID] = e
				entries = append(entries, e)
			}
			e.score += 1.0 / float64(rrfK+rank)
		}
	}
	if len(entries) == 0 {
		return []domain.KnowledgeHit{}
	}
	best := float64(len(lists)) / float64(rrfK+1)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score > entries[j].score
		}
		return entries[i].order < entries[j].order
	})
	if k > 0 && len(entries) > k {
		entries = entries[:k]
	}
	out := make([]domain.KnowledgeHit, len(entries))
	for i, e := range entries {
		out[i] = e.hit
		s := e.score / best
		if s > 1 {
			s = 1
		}
		out[i].Score = float32(s)
	}
	return out
}
