package crdt

import (
	"fmt"
	"sort"
)

// seqKey orders sibling vertices beneath one parent. Operation-created
// elements sort before initial elements so that "insert after P" places the
// new vertex between P and P's original successor; among concurrent inserts
// after the same parent, the newest operation sorts closest to the parent
// (standard RGA order). Initial elements keep base order.
type seqKey struct {
	initial bool
	stamp   Stamp // set for operation-created elements
	pos     int   // set for initial elements
}

func initialKey(pos int) seqKey {
	return seqKey{initial: true, pos: pos}
}

func opKey(stamp Stamp) seqKey {
	return seqKey{stamp: stamp}
}

// before reports whether a orders ahead of b among siblings.
func (a seqKey) before(b seqKey) bool {
	if a.initial != b.initial {
		return !a.initial
	}
	if a.initial {
		return a.pos < b.pos
	}
	return b.stamp.less(a.stamp)
}

// element is one vertex in the ordered tree, including tombstoned vertices
// that remain as stable insertion anchors.
type element struct {
	id       string
	parentID string // "" parents to the virtual ring root
	key      seqKey
	coord    [3]float64
	deleted  bool
	moveReg  Stamp // last-writer register for coord; zero = base coordinate
	children []*element
}

// vertexSeq is a convergent ordered sequence of vertices. Inserts are
// commutative given their parent exists, moves are last-writer-wins
// registers, and deletes are monotone tombstones — so operations may be
// applied incrementally in any order without replay.
type vertexSeq struct {
	byID  map[string]*element
	roots []*element // children of the virtual root, ordered by seqKey
	// visible caches the number of non-tombstoned vertices.
	visible int
}

func newVertexSeq() *vertexSeq {
	return &vertexSeq{byID: make(map[string]*element)}
}

// newInitialSeq builds a sequence from base coordinates using deterministic
// initial vertex IDs. Initial vertices form a parent chain in base order.
func newInitialSeq(ringIndex int, coords [][3]float64) *vertexSeq {
	s := newVertexSeq()
	parentID := ""
	for pos, coord := range coords {
		id := InitialVertexID(ringIndex, pos)
		s.mustInsert(id, parentID, initialKey(pos), coord)
		parentID = id
	}
	return s
}

func (s *vertexSeq) has(id string) bool {
	_, ok := s.byID[id]
	return ok
}

// insert adds a vertex under the given parent. It reports whether the state
// changed: repeated inserts of the same ID are idempotent no-ops. The caller
// must ensure the parent exists (buffering the operation otherwise).
func (s *vertexSeq) insert(id, parentID string, key seqKey, coord [3]float64) bool {
	if _, exists := s.byID[id]; exists {
		return false
	}
	el := &element{id: id, parentID: parentID, key: key, coord: coord}
	s.byID[id] = el

	siblings := s.roots
	if parentID != "" {
		siblings = s.byID[parentID].children
	}
	at := sort.Search(len(siblings), func(i int) bool {
		return key.before(siblings[i].key)
	})
	siblings = append(siblings, nil)
	copy(siblings[at+1:], siblings[at:])
	siblings[at] = el
	if parentID != "" {
		s.byID[parentID].children = siblings
	} else {
		s.roots = siblings
	}
	s.visible++
	return true
}

func (s *vertexSeq) mustInsert(id, parentID string, key seqKey, coord [3]float64) {
	if !s.insert(id, parentID, key, coord) {
		panic(fmt.Sprintf("crdt: duplicate initial vertex id %q", id))
	}
}

// move updates a vertex coordinate if the stamp wins the vertex's
// last-writer register. Moves apply to tombstoned vertices as well: the
// register must stay monotone so that replicas agree if the vertex is ever
// compared again.
func (s *vertexSeq) move(id string, stamp Stamp, coord [3]float64) bool {
	el, ok := s.byID[id]
	if !ok {
		return false
	}
	if el.moveReg.isSet() && !stamp.newer(el.moveReg) {
		return false
	}
	el.coord = coord
	el.moveReg = stamp
	return true
}

// delete tombstones a vertex. Tombstones are permanent: the vertex remains
// as an insertion anchor but is excluded from visible traversals.
func (s *vertexSeq) delete(id string) bool {
	el, ok := s.byID[id]
	if !ok || el.deleted {
		return false
	}
	el.deleted = true
	s.visible--
	return true
}

// walk visits every element (including tombstones) in document order.
func (s *vertexSeq) walk(visit func(*element)) {
	// Iterative DFS: push children in reverse so the first-ordered sibling
	// pops first. Recursion is avoided because insert chains can be as deep
	// as the vertex count.
	stack := make([]*element, 0, len(s.byID))
	for i := len(s.roots) - 1; i >= 0; i-- {
		stack = append(stack, s.roots[i])
	}
	for len(stack) > 0 {
		el := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		visit(el)
		for i := len(el.children) - 1; i >= 0; i-- {
			stack = append(stack, el.children[i])
		}
	}
}

// visibleCoords returns the coordinates of non-tombstoned vertices in order.
func (s *vertexSeq) visibleCoords() [][3]float64 {
	coords := make([][3]float64, 0, s.visible)
	s.walk(func(el *element) {
		if !el.deleted {
			coords = append(coords, el.coord)
		}
	})
	return coords
}

// visibleVertices returns the IDs and coordinates of non-tombstoned vertices
// in order.
func (s *vertexSeq) visibleVertices() []VertexInfo {
	vertices := make([]VertexInfo, 0, s.visible)
	s.walk(func(el *element) {
		if !el.deleted {
			vertices = append(vertices, VertexInfo{
				ID:    el.id,
				Coord: Coord{X: el.coord[0], Y: el.coord[1], Z: el.coord[2]},
			})
		}
	})
	return vertices
}

// vertexIDAt resolves a visible vertex index to its stable ID.
func (s *vertexSeq) vertexIDAt(index int) (string, error) {
	if index < 0 || index >= s.visible {
		return "", fmt.Errorf("vertex index %d out of range [0, %d)", index, s.visible)
	}
	id := ""
	seen := 0
	s.walk(func(el *element) {
		if el.deleted || id != "" {
			return
		}
		if seen == index {
			id = el.id
		}
		seen++
	})
	return id, nil
}
