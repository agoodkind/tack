package audit

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// newChainLink copies a row's chain coordinates and hashes into the fixed-width
// record for the chain pass. Each hash copy stops at the destination's size. A
// RowHash with a different length cannot equal the recomputed SHA-256 hash and
// fails the preceding row check. That check does not validate PrevHash length;
// a first row can omit PrevHash.
func newChainLink(row Row) chainLink {
	link := chainLink{
		Shard: row.Shard, Seq: row.Seq, EventID: row.EventID,
		PrevHash: [sha256.Size]byte{}, RowHash: [sha256.Size]byte{},
	}
	copy(link.PrevHash[:], row.PrevHash)
	copy(link.RowHash[:], row.RowHash)
	return link
}

// verifyChainLinks sorts links by shard and sequence. It reports repeated
// sequences and mismatched prev_hash links between adjacent sequence numbers,
// and it counts gaps without treating them as breaks.
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
			// Report a repeated or decreasing sequence as a break. Treating a replay as an
			// allowed gap would conceal it.
			if link.Seq <= lastSeqByShard[link.Shard] {
				report.ChainBreaks = append(report.ChainBreaks,
					fmt.Sprintf("row %s repeats sequence %d on shard %d", link.EventID, link.Seq, link.Shard))
			}
			// Count gaps without reporting breaks. Filtered exports can omit sequence
			// numbers, and a gap alone does not establish tampering.
			if link.Seq > lastSeqByShard[link.Shard]+1 {
				report.ChainGapCount++
			}
			if link.Seq == lastSeqByShard[link.Shard]+1 && link.PrevHash != lastHashByShard[link.Shard] {
				report.ChainBreaks = append(report.ChainBreaks,
					fmt.Sprintf("row %s has previous-hash link mismatch", link.EventID))
			}
		}
		// Advance tracking for every row, including a row with a failed hash. Advancing
		// only after a match would compare the next row with the wrong predecessor and
		// falsely report later rows as broken. That cascade would obscure the actual
		// extent of an edit.
		lastSeqByShard[link.Shard] = link.Seq
		lastHashByShard[link.Shard] = link.RowHash
		seenShard[link.Shard] = true
	}
}
