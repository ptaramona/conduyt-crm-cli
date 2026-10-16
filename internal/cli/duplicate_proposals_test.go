package cli

import (
	"strings"
	"testing"
)

// Identity rules v2 (board #69, app main 61bf61876): GET /contacts/{id}/duplicate-proposals and POST /contacts/{id}/duplicate-proposals/{proposalId}.
// Hand-written: --decision, its strict validation and the UUID checks (a reprint must keep them).
const (
	dpContact  = "3f8a0c4e-1111-4222-8333-444455556666"
	dpProposal = "9b2d7c1a-5e4f-4a3b-9c8d-7e6f5a4b3c2d"
)

func TestDuplicateProposalsGetSendsGETOnTheContactPath(t *testing.T) {
	reqs, err := runUnknownCaller(t, newContactsDuplicateProposalsGetContactsIdCmd, dpContact)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].method != "GET" || reqs[0].path != "/api/v1/contacts/"+dpContact+"/duplicate-proposals" || len(reqs[0].query) != 0 {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestDuplicateProposalsGetRefusesANonUUIDBeforeAnyRequest(t *testing.T) {
	for _, id := range []string{"../x", "nope", "3f8a0c4e-1111-4222-8333"} {
		reqs, err := runUnknownCaller(t, newContactsDuplicateProposalsGetContactsIdCmd, id)
		if err == nil || len(reqs) != 0 {
			t.Errorf("%q: err=%v requests=%d", id, err, len(reqs))
		}
	}
}

func TestDuplicateProposalsDecideSendsOnlyTheDecision(t *testing.T) {
	for _, d := range []string{"not_sure", "different", "merge"} {
		reqs, err := runUnknownCaller(t, newContactsDuplicateProposalsPostContactsIdProposalIdCmd, dpContact, dpProposal, "--decision", d)
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		if len(reqs) != 1 || reqs[0].method != "POST" || reqs[0].path != "/api/v1/contacts/"+dpContact+"/duplicate-proposals/"+dpProposal {
			t.Fatalf("%s: requests = %+v", d, reqs)
		}
		if len(reqs[0].body) != 1 || reqs[0].body["decision"] != d {
			t.Errorf("%s: body = %v", d, reqs[0].body)
		}
	}
}

func TestDuplicateProposalsDecideIsStrictBeforeAnyRequest(t *testing.T) {
	cases := map[string][]string{
		"no decision":      {dpContact, dpProposal},
		"merged":           {dpContact, dpProposal, "--decision", "merged"},
		"upper case":       {dpContact, dpProposal, "--decision", "Different"},
		"open":             {dpContact, dpProposal, "--decision", "open"},
		"empty":            {dpContact, dpProposal, "--decision", ""},
		"bad contact id":   {"nope", dpProposal, "--decision", "different"},
		"bad proposal id":  {dpContact, "../x", "--decision", "different"},
		"missing proposal": {dpContact, "--decision", "different"},
	}
	for name, args := range cases {
		reqs, err := runUnknownCaller(t, newContactsDuplicateProposalsPostContactsIdProposalIdCmd, args...)
		if err == nil || len(reqs) != 0 {
			t.Errorf("%s: err=%v requests=%d", name, err, len(reqs))
		}
	}
}

func TestDuplicateProposalsDecideHelpNamesTheMeanings(t *testing.T) {
	cmd := newContactsDuplicateProposalsPostContactsIdProposalIdCmd(&rootFlags{})
	for _, want := range []string{"not_sure", "different", "FINAL", "never merges", "not_merged", "already_decided", "other_record_not_editable"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("Long is missing %q", want)
		}
	}
}

func TestContactsGroupRegistersDuplicateProposals(t *testing.T) {
	cmd := newContactsCmd(&rootFlags{})
	sub, _, err := cmd.Find([]string{"duplicate-proposals", "post-contacts-id-proposal-id"})
	if err != nil || sub == nil || sub.Name() != "post-contacts-id-proposal-id" {
		t.Fatalf("find: %v %v", sub, err)
	}
}
