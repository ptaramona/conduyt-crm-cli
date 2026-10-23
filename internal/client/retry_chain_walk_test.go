// PATCH(retry-policy): regressions for the chain walk's nil children, nested joins and frame-stack bound (#398).

package client

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// namedErr is a leaf with a stable label so visit order is readable.
type namedErr string

func (n namedErr) Error() string { return string(n) }

// joinOf is a labelled errors.Join-style node; kids may contain nil.
type joinOf struct {
	label string
	kids  []error
}

func (j joinOf) Error() string   { return j.label }
func (j joinOf) Unwrap() []error { return j.kids }

func walkLabels(root error) (labels []string, peak int) {
	_, peak = walkChainPeak(root, func(n error) bool {
		labels = append(labels, n.Error())
		return false
	})
	return labels, peak
}

// Mutant: dropping the `n == nil` guard after the frame pull (or visiting a
// nil child) panics or double-counts; moving `seen++` above the nil check
// would count nil slots against the budget.
func TestWalkChainNilChildrenAreSkipped(t *testing.T) {
	a, b, c := namedErr("a"), namedErr("b"), namedErr("c")
	cases := []struct {
		name string
		root error
		want string
	}{
		{"nil root", nil, ""},
		{"nil first child", joinOf{"J", []error{nil, a, b}}, "J,a,b"},
		{"nil middle child", joinOf{"J", []error{a, nil, b}}, "J,a,b"},
		{"nil last child", joinOf{"J", []error{a, b, nil}}, "J,a,b"},
		{"all nil children", joinOf{"J", []error{nil, nil, nil}}, "J"},
		{"nil run between leaves", joinOf{"J", []error{a, nil, nil, nil, b, nil, c}}, "J,a,b,c"},
		{"nil in a nested leaf list", joinOf{"J", []error{joinOf{"K", []error{nil, a, nil}}, nil, b}}, "J,K,a,b"},
		{"nil last child of the inner join", joinOf{"J", []error{joinOf{"K", []error{a, nil}}, b}}, "J,K,a,b"},
		{"wrapped nil is not visited twice", joinOf{"J", []error{fmt.Errorf("w: %w", a), nil}}, "J,w: a,a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			labels, _ := walkLabels(tc.root)
			if got := strings.Join(labels, ","); got != tc.want {
				t.Fatalf("visited %q, want %q", got, tc.want)
			}
		})
	}
}

// Mutant: eager child queuing (appending all kids to a slice) or popping a
// frame before its last child's subtree is done would reorder or repeat nodes.
func TestWalkChainNestedJoinsVisitInIsOrderOnce(t *testing.T) {
	a, b, c, d, e, f := namedErr("a"), namedErr("b"), namedErr("c"), namedErr("d"), namedErr("e"), namedErr("f")
	deep := joinOf{"J3", []error{e, f}}
	mid := joinOf{"J2", []error{b, deep, c}}
	top := joinOf{"J1", []error{a, mid, d, joinOf{"J4", []error{joinOf{"J5", []error{namedErr("g")}}}}}}
	labels, peak := walkLabels(top)
	want := "J1,a,J2,b,J3,e,f,c,d,J4,J5,g"
	if got := strings.Join(labels, ","); got != want {
		t.Fatalf("visit order %q, want %q", got, want)
	}
	seen := map[string]bool{}
	for _, l := range labels {
		if seen[l] {
			t.Fatalf("node %q visited twice", l)
		}
		seen[l] = true
	}
	// Three levels of join are open at once at the deepest point (J1, J2, J3).
	if peak != 3 {
		t.Fatalf("peak frame stack %d, want 3 for three open join levels", peak)
	}
	// The walk must agree with errors.Is: every node is findable in that order.
	for _, target := range []error{a, b, e, f, c, d} {
		if !errors.Is(top, target) {
			t.Fatalf("errors.Is lost %v", target)
		}
		if !walkChain(top, func(n error) bool { return n == target }) {
			t.Fatalf("walkChain did not find %v", target)
		}
	}
}

// Mutant: pushing a frame per child (or per visit) instead of per join, or
// failing to bound it, makes the one-level peak 2 or more.
func TestWalkChainOneLevelPeakIsExactlyOne(t *testing.T) {
	for _, n := range []int{1, 2, 50, maxUnwrapNodes - 1, maxUnwrapNodes * 3} {
		kids := make([]error, n)
		for i := range kids {
			kids[i] = namedErr("k")
		}
		_, peak := walkChainPeak(joinOf{"J", kids}, func(error) bool { return false })
		if peak != 1 {
			t.Fatalf("%d kids: peak %d, want exactly 1", n, peak)
		}
	}
	// A join with no children never pushes a frame; a plain chain never does.
	if _, peak := walkChainPeak(joinOf{"J", nil}, func(error) bool { return false }); peak != 0 {
		t.Fatalf("empty join peak %d, want 0", peak)
	}
	if _, peak := walkChainPeak(fmt.Errorf("x: %w", namedErr("y")), func(error) bool { return false }); peak != 0 {
		t.Fatalf("wrap chain peak %d, want 0", peak)
	}
}

// Mutant: raising or removing the maxUnwrapNodes check lets a deep or cyclic
// chain run past the budget; letting frames outgrow the visited count breaks
// the documented memory bound (depth plus one frame per join).
func TestWalkChainDeepChainsStayWithinBudgetAndFrameBound(t *testing.T) {
	// Deep chain of single-child joins, longer than the budget.
	var deep error = namedErr("leaf")
	for i := 0; i < maxUnwrapNodes*2; i++ {
		deep = joinOf{"J", []error{deep}}
	}
	visited := 0
	found, peak := walkChainPeak(deep, func(error) bool { visited++; return false })
	if found {
		t.Fatal("unexpected match")
	}
	if visited != maxUnwrapNodes {
		t.Fatalf("visited %d, want exactly the budget %d", visited, maxUnwrapNodes)
	}
	if peak < 1 || peak > maxUnwrapNodes {
		t.Fatalf("peak %d outside 1..%d (one frame per visited join at most)", peak, maxUnwrapNodes)
	}

	// Deep wrap chain: budget holds, no frames at all.
	var wrap error = namedErr("leaf")
	for i := 0; i < maxUnwrapNodes*2; i++ {
		wrap = fmt.Errorf("w: %w", wrap)
	}
	visited = 0
	_, peak = walkChainPeak(wrap, func(error) bool { visited++; return false })
	if visited != maxUnwrapNodes || peak != 0 {
		t.Fatalf("wrap chain visited %d peak %d, want %d and 0", visited, peak, maxUnwrapNodes)
	}

	// A leaf past the budget is not seen.
	if walkChain(wrap, func(n error) bool { return n == namedErr("leaf") }) {
		t.Fatal("leaf beyond the budget must not be reached")
	}
}
