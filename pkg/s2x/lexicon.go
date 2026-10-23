package s2x

import (
	"math"
	"slices"
	"unsafe"
)

// emptySetID is IdSetLexicon::EmptySetId().
const emptySetID int32 = math.MinInt32

// idSetLexicon is IdSetLexicon: it maps sets of non-negative ids to int32
// set ids. Singletons map to the id itself, the empty set to emptySetID and
// larger sets to the complement of a sequence id.
type idSetLexicon struct {
	seqs  [][]int32
	index map[string]int32
}

func newIDSetLexicon() *idSetLexicon { return &idSetLexicon{index: map[string]int32{}} }

func (l *idSetLexicon) clear() {
	l.seqs = nil
	l.index = map[string]int32{}
}

func seqKey(ids []int32) string {
	if len(ids) == 0 {
		return ""
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(&ids[0])), len(ids)*4))
}

// add returns the id of the set of ids (which it may reorder).
func (l *idSetLexicon) add(ids []int32) int32 {
	switch len(ids) {
	case 0:
		return emptySetID
	case 1:
		return ids[0]
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 1 {
		return ids[0]
	}
	k := seqKey(ids)
	if id, ok := l.index[k]; ok {
		return ^id
	}
	id := int32(len(l.seqs))
	l.seqs = append(l.seqs, slices.Clone(ids))
	l.index[k] = id
	return ^id
}

func (l *idSetLexicon) addSingleton(id int32) int32 { return id }

// idSet returns the sorted ids of a set id.
func (l *idSetLexicon) idSet(setID int32) []int32 {
	if setID >= 0 {
		return []int32{setID}
	}
	if setID == emptySetID {
		return nil
	}
	return l.seqs[^setID]
}
