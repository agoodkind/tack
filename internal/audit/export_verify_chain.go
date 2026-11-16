package audit

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// newChainLink copies chain coordinates and hashes into fixed-width storage.
// The row hash check already reports invalid hash lengths.
func newChainLink(row Row) chainLink {
	link := chainLink{
		Shard:    row.Shard,
		Seq:      row.Seq,
		EventID:  row.EventID,
		PrevHash: [sha256.Size]byte{},
		RowHash:  [sha256.Size]byte{},
	}
	copy(link.PrevHash[:], row.PrevHash)
	copy(link.RowHash[:], row.RowHash)

	return link
}

// verifyChainLinks checks ascending per-shard sequences and immediate
// predecessor hashes.
func verifyChainLinks(report *VerifyReport, links []chainLink) {
	sort.SliceStable(
		links,
		func(i, j int) bool {
			if links[i].Shard != links[j].Shard {
				return links[i].Shard < links[j].Shard
			}

			return links[i].Seq < links[j].Seq
		},
	)
	lastSeqByShard := map[int16]int64{}
	lastHashByShard := map[int16][sha256.Size]byte{}
	seenShard := map[int16]bool{}

	for _, link := range links {
		if seenShard[link.Shard] {
			// Repeated or decreasing sequences are breaks, not filtered gaps.
			if link.Seq <= lastSeqByShard[link.Shard] {
				report.ChainBreaks = append(
					report.ChainBreaks,
					fmt.Sprintf(
						"row %s repeats sequence %d on shard %d",
						link.EventID,
						link.Seq,
						link.Shard,
					),
				)
			}

			// Filtered exports can skip sequences without indicating tampering.
			if link.Seq > lastSeqByShard[link.Shard]+1 {
				report.ChainGapCount++
			}

			if link.Seq == lastSeqByShard[link.Shard]+1 &&
				link.PrevHash != lastHashByShard[link.Shard] {
				report.ChainBreaks = append(
					report.ChainBreaks,
					fmt.Sprintf("row %s has previous-hash link mismatch", link.EventID),
				)
			}
		}

		// Track every row, even after a hash mismatch. The next row must compare
		// against its actual predecessor instead of reporting a cascade of failures.
		lastSeqByShard[link.Shard] = link.Seq
		lastHashByShard[link.Shard] = link.RowHash
		seenShard[link.Shard] = true
	}
}
