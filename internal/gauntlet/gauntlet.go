// Package gauntlet runs a sealed, fixed-order first-battle examination.
// It deliberately accepts only commitments (virtual hashes), never market
// records or threshold-level grading material.
package gauntlet

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/exam"
)

type Role string

const (
	RoleResearcher Role = "researcher"
	RoleExaminer   Role = "examiner"
	RoleCustodian  Role = "custodian"
)

// AccessController is the sole authorization seam. Implementations must not
// disclose hidden-paper metadata when denying a request.
type AccessController interface {
	Allow(role Role, action string) bool
}

type FixedAccess struct{}

func (FixedAccess) Allow(role Role, action string) bool {
	switch action {
	case "create_generation":
		return role == RoleCustodian
	case "unseal_next", "grade":
		return role == RoleExaminer
	case "view_receipt":
		return role == RoleResearcher || role == RoleExaminer || role == RoleCustodian
	default:
		return false
	}
}

type Kind string

const (
	KindStandard Kind = "standard"
	KindGolden   Kind = "golden"
)

type GenerationStatus string

const (
	GenerationPlanned GenerationStatus = "planned"
	GenerationRunning GenerationStatus = "running"
	GenerationPassed  GenerationStatus = "passed"
	GenerationFailed  GenerationStatus = "failed"
)

// Ticket is a commitment-only description of a hidden paper. Its hashes are
// virtual commitments in tests; this package never opens a data source.
type Ticket struct {
	ID            string `json:"id"`
	Queue         int    `json:"queue"`
	Kind          Kind   `json:"kind"`
	Family        string `json:"family"`
	AlphaSpend    int    `json:"alpha_spend"`
	ChallengeHash string `json:"challenge_hash"`
	ProtocolHash  string `json:"protocol_hash"`
	DataHash      string `json:"data_hash"`
}

type FamilyBudget struct {
	Limit int `json:"limit"`
	Spent int `json:"spent"`
}

type Generation struct {
	ID           string                  `json:"id"`
	Status       GenerationStatus        `json:"status"`
	Tickets      []Ticket                `json:"tickets"`
	Exams        []exam.Exam             `json:"exams"`
	Budgets      map[string]FamilyBudget `json:"budgets"`
	ManifestHash string                  `json:"manifest_hash"`
}

// State is intentionally not a paper catalogue: it only retains commitments
// and consumption receipts. There is no listing API for researcher roles.
type State struct {
	Generations []Generation `json:"generations"`
}

// SignedResult is the only result intended for researchers. It omits paper
// identifiers, raw metrics, grade thresholds, and itemized answers.
type SignedResult struct {
	Generation string `json:"generation"`
	Pass       bool   `json:"pass"`
	Reason     string `json:"reason"`
	KeyID      string `json:"key_id,omitempty"`
	Algorithm  string `json:"algorithm,omitempty"`
	Signature  string `json:"signature"`
}

var (
	ErrForbidden        = errors.New("gauntlet access denied")
	ErrGenerationLocked = errors.New("generation schedule is immutable")
)

func CreateGeneration(s State, access AccessController, role Role, id string, tickets []Ticket, budgets map[string]int, manifestHash string, now time.Time) (State, error) {
	if !allowed(access, role, "create_generation") {
		return State{}, ErrForbidden
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(manifestHash) == "" {
		return State{}, errors.New("generation id and manifest hash are required")
	}
	if _, _, ok := generation(s, id); ok {
		return State{}, ErrGenerationLocked
	}
	if err := validateSchedule(s, tickets, budgets); err != nil {
		return State{}, err
	}
	ordered := cloneTickets(tickets)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Queue < ordered[j].Queue })
	g := Generation{ID: id, Status: GenerationPlanned, Tickets: ordered, Budgets: make(map[string]FamilyBudget), ManifestHash: manifestHash}
	for _, ticket := range g.Tickets {
		draft := exam.NewDraft(ticket.ID, id, ticket.Queue, ticket.ChallengeHash, ticket.ProtocolHash, ticket.DataHash, now)
		sealed, err := exam.Seal(draft, now)
		if err != nil {
			return State{}, err
		}
		g.Exams = append(g.Exams, sealed)
		g.Budgets[ticket.Family] = FamilyBudget{Limit: budgets[ticket.Family]}
	}
	s.Generations = append(append([]Generation(nil), s.Generations...), g)
	return s, nil
}

// UnsealNext starts exactly the next fixed-order paper. A golden paper may
// start only after every standard paper has passed, making it a separate final
// attempt rather than another tunable checkpoint.
func UnsealNext(s State, access AccessController, role Role, generationID string, now time.Time) (State, exam.Exam, error) {
	if !allowed(access, role, "unseal_next") {
		return State{}, exam.Exam{}, ErrForbidden
	}
	index, g, ok := generation(s, generationID)
	if !ok {
		return State{}, exam.Exam{}, errors.New("generation not found")
	}
	if g.Status == GenerationFailed || g.Status == GenerationPassed {
		return State{}, exam.Exam{}, errors.New("generation is terminal")
	}
	for _, e := range g.Exams {
		if e.Status == exam.StatusRunning {
			return State{}, exam.Exam{}, errors.New("a paper is already unsealed")
		}
	}
	nextIndex := nextSealedIndex(g.Exams)
	if nextIndex < 0 {
		return State{}, exam.Exam{}, errors.New("no untouched paper available")
	}
	if g.Tickets[nextIndex].Kind == KindGolden && !allStandardPassed(g) {
		return State{}, exam.Exam{}, errors.New("golden paper is available only after all standard papers pass")
	}
	budget := g.Budgets[g.Tickets[nextIndex].Family]
	if budget.Spent+g.Tickets[nextIndex].AlphaSpend > budget.Limit {
		return State{}, exam.Exam{}, errors.New("family alpha-spending budget exhausted")
	}
	started, err := exam.Start(g.Exams[nextIndex], now)
	if err != nil {
		return State{}, exam.Exam{}, err
	}
	g.Exams[nextIndex] = started
	g.Status = GenerationRunning
	budget.Spent += g.Tickets[nextIndex].AlphaSpend // unsealing spends budget even on crash.
	g.Budgets[g.Tickets[nextIndex].Family] = budget
	s.Generations[index] = g
	return s, started, nil
}

// Grade consumes the currently unsealed paper. The examiner supplies only a
// standardized coarse reason; detailed grade evidence never crosses this API.
func Grade(ctx context.Context, s State, access AccessController, role Role, generationID string, pass bool, reason string, now time.Time, signer exam.ReceiptSigner) (State, SignedResult, error) {
	if !allowed(access, role, "grade") {
		return State{}, SignedResult{}, ErrForbidden
	}
	if !validReason(reason) {
		return State{}, SignedResult{}, errors.New("reason must be a standardized coarse code")
	}
	index, g, ok := generation(s, generationID)
	if !ok {
		return State{}, SignedResult{}, errors.New("generation not found")
	}
	running := -1
	for i, e := range g.Exams {
		if e.Status == exam.StatusRunning {
			running = i
			break
		}
	}
	if running < 0 {
		return State{}, SignedResult{}, errors.New("no unsealed paper to grade")
	}
	consumed, err := exam.Consume(ctx, g.Exams[running], exam.Verdict{Passed: pass, Reason: reason}, now, signer)
	if err != nil {
		return State{}, SignedResult{}, err
	}
	g.Exams[running] = consumed
	if !pass {
		g.Status = GenerationFailed
	} else if g.Tickets[running].Kind == KindGolden {
		g.Status = GenerationPassed
	}
	s.Generations[index] = g
	return s, researcherResult(generationID, consumed.Verdict, *consumed.Receipt), nil
}

// Crash seals an interrupted unsealed paper as consumed_crashed. Recovery must
// call this rather than reopening the paper, so crashes cannot create retries.
func Crash(ctx context.Context, s State, access AccessController, role Role, generationID string, now time.Time, signer exam.ReceiptSigner) (State, SignedResult, error) {
	if !allowed(access, role, "grade") {
		return State{}, SignedResult{}, ErrForbidden
	}
	index, g, ok := generation(s, generationID)
	if !ok {
		return State{}, SignedResult{}, errors.New("generation not found")
	}
	running := -1
	for i, e := range g.Exams {
		if e.Status == exam.StatusRunning {
			running = i
			break
		}
	}
	if running < 0 {
		return State{}, SignedResult{}, errors.New("no unsealed paper to crash")
	}
	consumed, err := exam.Crash(ctx, g.Exams[running], "CRASH", now, signer)
	if err != nil {
		return State{}, SignedResult{}, err
	}
	g.Exams[running] = consumed
	g.Status = GenerationFailed
	s.Generations[index] = g
	return s, researcherResult(generationID, consumed.Verdict, *consumed.Receipt), nil
}

func ResearchReceipt(s State, access AccessController, role Role, generationID string) (SignedResult, error) {
	if !allowed(access, role, "view_receipt") {
		return SignedResult{}, ErrForbidden
	}
	_, g, ok := generation(s, generationID)
	if !ok {
		return SignedResult{}, errors.New("generation not found")
	}
	for i := len(g.Exams) - 1; i >= 0; i-- {
		e := g.Exams[i]
		if e.Receipt != nil && e.Verdict != nil {
			return researcherResult(generationID, e.Verdict, *e.Receipt), nil
		}
	}
	return SignedResult{}, errors.New("no signed result available")
}

func validateSchedule(s State, tickets []Ticket, budgets map[string]int) error {
	if len(tickets) < 2 {
		return errors.New("a gauntlet needs at least one standard and one golden paper")
	}
	if err := validateNoCommitmentReuse(s, tickets); err != nil {
		return err
	}
	seen := make(map[string]bool)
	sorted := cloneTickets(tickets)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Queue < sorted[j].Queue })
	for i, ticket := range sorted {
		if ticket.ID == "" || ticket.Queue <= 0 || ticket.Family == "" || ticket.AlphaSpend <= 0 || ticket.ChallengeHash == "" || ticket.ProtocolHash == "" || ticket.DataHash == "" {
			return errors.New("each ticket needs non-empty virtual commitments, family, and positive alpha spend")
		}
		if seen[ticket.ID] || (i > 0 && ticket.Queue == sorted[i-1].Queue) {
			return errors.New("ticket ids and queue positions must be unique")
		}
		seen[ticket.ID] = true
		if ticket.Kind != KindStandard && ticket.Kind != KindGolden {
			return errors.New("ticket kind must be standard or golden")
		}
		if ticket.Kind == KindGolden && i != len(sorted)-1 {
			return errors.New("golden paper must be final")
		}
		if budgets[ticket.Family] < ticket.AlphaSpend {
			return fmt.Errorf("family %q budget cannot fund its next paper", ticket.Family)
		}
	}
	if countGolden(sorted) != 1 {
		return errors.New("exactly one golden paper is required")
	}
	if previous, failed := latestFailed(s); failed && sorted[0].Queue != previous+1 {
		return fmt.Errorf("new generation must start with next untouched queue %d", previous+1)
	}
	return nil
}

// validateNoCommitmentReuse forbids reusing a ChallengeHash or DataHash both
// within a schedule and across every prior generation. A sealed history paper
// commits to exactly one hidden dataset and one hidden challenge; reusing a
// commitment in a later generation would let a researcher re-answer a paper
// whose answer is already known, so the reuse is rejected fail-closed.
func validateNoCommitmentReuse(s State, tickets []Ticket) error {
	priorChallenge := make(map[string]string)
	priorData := make(map[string]string)
	for _, g := range s.Generations {
		for _, t := range g.Tickets {
			if t.ChallengeHash != "" {
				priorChallenge[t.ChallengeHash] = g.ID
			}
			if t.DataHash != "" {
				priorData[t.DataHash] = g.ID
			}
		}
	}
	localChallenge := make(map[string]bool)
	localData := make(map[string]bool)
	for _, t := range tickets {
		if t.ChallengeHash != "" {
			if owner, ok := priorChallenge[t.ChallengeHash]; ok {
				return fmt.Errorf("challenge hash already committed by generation %q; cross-generation reuse is forbidden", owner)
			}
			if localChallenge[t.ChallengeHash] {
				return errors.New("duplicate challenge hash within generation schedule")
			}
			localChallenge[t.ChallengeHash] = true
		}
		if t.DataHash != "" {
			if owner, ok := priorData[t.DataHash]; ok {
				return fmt.Errorf("data hash already committed by generation %q; cross-generation reuse is forbidden", owner)
			}
			if localData[t.DataHash] {
				return errors.New("duplicate data hash within generation schedule")
			}
			localData[t.DataHash] = true
		}
	}
	return nil
}

func latestFailed(s State) (int, bool) {
	max := 0
	failed := false
	for _, g := range s.Generations {
		if g.Status != GenerationFailed {
			continue
		}
		for _, e := range g.Exams {
			if e.ConsumedAt.IsZero() {
				continue
			}
			if e.Order > max {
				max = e.Order
			}
			failed = true
		}
	}
	return max, failed
}

func allStandardPassed(g Generation) bool {
	for i, ticket := range g.Tickets {
		if ticket.Kind == KindStandard && g.Exams[i].Status != exam.StatusConsumedPassed {
			return false
		}
	}
	return true
}

func nextSealedIndex(exams []exam.Exam) int {
	for i, e := range exams {
		if e.Status == exam.StatusSealed {
			return i
		}
	}
	return -1
}

func validReason(reason string) bool {
	switch reason {
	case "PASS", "ALPHA_BUDGET", "FAMILY_MULTIPLE_TESTING", "RISK_GATE", "ROBUSTNESS_GATE", "EXECUTION_GATE", "CRASH":
		return true
	default:
		return false
	}
}

func countGolden(tickets []Ticket) int {
	n := 0
	for _, ticket := range tickets {
		if ticket.Kind == KindGolden {
			n++
		}
	}
	return n
}

func cloneTickets(tickets []Ticket) []Ticket { return append([]Ticket(nil), tickets...) }

func generation(s State, id string) (int, Generation, bool) {
	for i, g := range s.Generations {
		if g.ID == id {
			return i, g, true
		}
	}
	return 0, Generation{}, false
}

func allowed(access AccessController, role Role, action string) bool {
	return access != nil && access.Allow(role, action)
}

func researcherResult(generation string, verdict *exam.Verdict, receipt exam.Receipt) SignedResult {
	return SignedResult{
		Generation: generation,
		Pass:       verdict.Passed,
		Reason:     verdict.Reason,
		KeyID:      receipt.KeyID,
		Algorithm:  receipt.Algorithm,
		Signature:  receipt.Signature,
	}
}
