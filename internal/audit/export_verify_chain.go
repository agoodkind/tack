package audit

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// newChainLink copies a row's chain coordinates into the fixed-width record
// the link pass reads. A hash of unexpected length copies as far as it fits;
// the row's own hash check has already reported that row.
func newChainLink(row Row) chainLink {
	link := chainLink{
		Shard: row.Shard, Seq: row.Seq, EventID: row.EventID,
		PrevHash: [sha256.Size]byte{}, RowHash: [sha256.Size]byte{},
	}
	copy(link.PrevHash[:], row.PrevHash)
	copy(link.RowHash[:], row.RowHash)
	return link
}

// verifyChainLinks walks the links in per-shard sequence order and reports a
// break wherever a row's prev_hash does not name its immediate predecessor.
func verifyChainLinks(report *VerifyReport, links []chainLink) {
	sort.SliceStable(links, func(i, j int) bool {
		if links[i].Shard != links[j].Shard {
			return links[i].Shard < links[j].Shard
		}
		return links[i].Seq < links[j].Seq
	})
	lastSeqByShard := map[int16]int64{}
	lastHashByShard := map[int16][sha256.Size]byte{}
	seenShard := map[int16]bool{}
	for _, link := range links {
		if seenShard[link.Shard] {
			// A sequence that does not advance is a repeated or replayed row.
			// A gap-tolerant walk would count it as a gap and wave it through,
			// so it is named as a break instead.
			if link.Seq <= lastSeqByShard[link.Shard] {
				report.ChainBreaks = append(report.ChainBreaks,
					fmt.Sprintf("row %s repeats sequence %d on shard %d", link.EventID, link.Seq, link.Shard))
			}
			// A gap is counted, not reported as a break. An export is
			// filtered, so missing sequence numbers are the normal case and
			// say nothing about tampering.
			if link.Seq > lastSeqByShard[link.Shard]+1 {
				report.ChainGapCount++
			}
			if link.Seq == lastSeqByShard[link.Shard]+1 && link.PrevHash != lastHashByShard[link.Shard] {
				report.ChainBreaks = append(report.ChainBreaks,
					fmt.Sprintf("row %s has previous-hash link mismatch", link.EventID))
			}
		}
		// Tracking advances for every row, matched or not. Advancing only on
		// a match would make the next row compare itself against a row that
		// is not its predecessor, so one edited row would report every row
		// after it as broken too, and a reader could not tell how much of the
		// bundle was actually altered.
		lastSeqByShard[link.Shard] = link.Seq
		lastHashByShard[link.Shard] = link.RowHash
		seenShard[link.Shard] = true
	}
}
