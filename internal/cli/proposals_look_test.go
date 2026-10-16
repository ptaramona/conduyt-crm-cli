package cli

import (
	"strings"
	"testing"
)

// Duplicate proposals look (board #95, app f2d638db6): the reprint renamed the two contact commands, added the list's paging
// flags and the import rows command. Hand-written: the old names as aliases, the paging guards, send-only-given flags.
const dpImport = "7a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"

func TestDuplicateProposalsOldNamesStayAliases(t *testing.T) {
	cmd := newContactsCmd(&rootFlags{})
	for alias, name := range map[string]string{"get-contacts-id": "list-contact", "post-contacts-id-proposal-id": "decide-contact"} {
		sub, _, err := cmd.Find([]string{"duplicate-proposals", alias})
		if err != nil || sub == nil || sub.Name() != name {
			t.Errorf("%s: find = %v %v, want %s", alias, sub, err, name)
		}
	}
}

func TestDuplicateProposalsListSendsEachPagingFlag(t *testing.T) {
	cases := map[string]struct {
		args []string
		want map[string]string
	}{
		"cursor":      {[]string{"--cursor", "eyJ0Ijo+/="}, map[string]string{"cursor": "eyJ0Ijo+/="}},
		"limit":       {[]string{"--limit", "100"}, map[string]string{"limit": "100"}},
		"held-cursor": {[]string{"--held-cursor", "abc"}, map[string]string{"held_cursor": "abc"}},
		"held-limit":  {[]string{"--held-limit", "1"}, map[string]string{"held_limit": "1"}},
		"all four":    {[]string{"--cursor", "c1", "--limit", "10", "--held-cursor", "h0", "--held-limit", "7"}, map[string]string{"cursor": "c1", "limit": "10", "held_cursor": "h0", "held_limit": "7"}},
	}
	for name, tc := range cases {
		reqs, err := runUnknownCaller(t, newContactsDuplicateProposalsListContactCmd, append([]string{dpContact}, tc.args...)...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(reqs) != 1 || reqs[0].path != "/api/v1/contacts/"+dpContact+"/duplicate-proposals" || len(reqs[0].query) != len(tc.want) {
			t.Fatalf("%s: requests = %+v", name, reqs)
		}
		for k, v := range tc.want {
			if reqs[0].query.Get(k) != v {
				t.Errorf("%s: %s = %q, want %q", name, k, reqs[0].query.Get(k), v)
			}
		}
	}
}

func TestDuplicateProposalsListRefusesBadPagingBeforeAnyRequest(t *testing.T) {
	for name, args := range map[string][]string{
		"limit 0":           {"--limit", "0"},
		"limit 101":         {"--limit", "101"},
		"held-limit 0":      {"--held-limit", "0"},
		"held-limit 101":    {"--held-limit", "101"},
		"limit not int":     {"--limit", "2.5"},
		"blank cursor":      {"--cursor", " "},
		"empty held-cursor": {"--held-cursor", ""},
		"no --all":          {"--all"},
	} {
		reqs, err := runUnknownCaller(t, newContactsDuplicateProposalsListContactCmd, append([]string{dpContact}, args...)...)
		if err == nil || len(reqs) != 0 {
			t.Errorf("%s: err=%v requests=%d", name, err, len(reqs))
		}
	}
}

func TestDuplicateProposalsHelpDocumentsPagingAndRevisions(t *testing.T) {
	list := newContactsDuplicateProposalsListContactCmd(&rootFlags{})
	for _, want := range []string{"total", "nextCursor", "heldIdentifiersTotal", "heldIdentifiersNextCursor", "membershipRevision", "heldIdentifiersMembershipRevision", "read page one again", "invalid_cursor"} {
		if !strings.Contains(list.Long, want) {
			t.Errorf("list Long is missing %q", want)
		}
	}
	decide := newContactsDuplicateProposalsDecideContactCmd(&rootFlags{})
	for _, want := range []string{"previousMembershipRevision", "membershipRevision", "total", "AFTER", "BEFORE"} {
		if !strings.Contains(decide.Long, want) {
			t.Errorf("decide Long is missing %q", want)
		}
	}
}

func TestImportsDuplicateProposalsSendsGETAndOnlyGivenPaging(t *testing.T) {
	reqs, err := runUnknownCaller(t, newImportsDuplicateProposalsGetImportsIdCmd, dpImport)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].method != "GET" || reqs[0].path != "/api/v1/imports/"+dpImport+"/duplicate-proposals" || len(reqs[0].query) != 0 {
		t.Fatalf("bare: requests = %+v", reqs)
	}
	reqs, err = runUnknownCaller(t, newImportsDuplicateProposalsGetImportsIdCmd, dpImport, "--page", "2", "--per-page", "100")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].query.Get("page") != "2" || reqs[0].query.Get("per_page") != "100" || len(reqs[0].query) != 2 {
		t.Fatalf("paged: requests = %+v", reqs)
	}
}

func TestImportsDuplicateProposalsIsStrictBeforeAnyRequest(t *testing.T) {
	for name, args := range map[string][]string{
		"bad id":        {"nope"},
		"page 0":        {dpImport, "--page", "0"},
		"page 100001":   {dpImport, "--page", "100001"},
		"per-page 0":    {dpImport, "--per-page", "0"},
		"per-page 101":  {dpImport, "--per-page", "101"},
		"cursor":        {dpImport, "--cursor", "x"},
		"no --all flag": {dpImport, "--all"},
	} {
		reqs, err := runUnknownCaller(t, newImportsDuplicateProposalsGetImportsIdCmd, args...)
		if err == nil || len(reqs) != 0 {
			t.Errorf("%s: err=%v requests=%d", name, err, len(reqs))
		}
	}
}

func TestImportsGroupRegistersDuplicateProposals(t *testing.T) {
	cmd := newImportsCmd(&rootFlags{})
	sub, _, err := cmd.Find([]string{"duplicate-proposals", "get-imports-id"})
	if err != nil || sub == nil || sub.Name() != "get-imports-id" {
		t.Fatalf("find: %v %v", sub, err)
	}
	for _, want := range []string{"A contact you cannot view", "merged", "decide-contact", "newRecord", "matchedOn", "never a value", "contacts:view"} {
		if !strings.Contains(sub.Long, want) {
			t.Errorf("Long is missing %q", want)
		}
	}
}
