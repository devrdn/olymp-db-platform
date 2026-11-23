package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrBudgetSpent is wrapped by every refusal Charge returns. The *Refusal it
// comes in names the code to answer with and how long to wait.
var ErrBudgetSpent = errors.New("request budget spent")

// ErrBudgetUnavailable is a counter that could not be kept. The request is
// refused all the same: without the counter, refusing is safe.
var ErrBudgetUnavailable = errors.New("request budget could not be counted")

// Counter counts attempts per subject in a fixed window (auth.Limiter). A
// counter that cannot count answers (false, err).
type Counter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// keyKind is how many keys a budget can be made to keep, which decides the
// order rule 5 charges it in.
type keyKind int

const (
	// keyAddress is one key per machine, or per IPv6 /64 (ClientSubject).
	keyAddress keyKind = iota
	// keyResolved is an account, a registration or a contest: bounded by the
	// table that holds it.
	keyResolved
	// keyTyped is an email or a login as the caller typed it: one key per
	// string the caller invents.
	keyTyped
)

type budget struct {
	name    string
	kind    keyKind
	limit   int
	window  time.Duration
	code    Code
	message string
}

func declare(name string, kind keyKind, limit int, window time.Duration, code Code, message string) budget {
	if limit < 1 || window <= 0 {
		panic(fmt.Sprintf("httpx: budget %q admits %d in %s, so it could never admit anything", name, limit, window))
	}
	return budget{name: name, kind: kind, limit: limit, window: window, code: code, message: message}
}

// AddressBudget is charged per client address.
type AddressBudget struct{ budget budget }

// KeyBudget is charged per key the handler names.
type KeyBudget struct{ budget budget }

// PerAddress declares a budget of limit requests per client address in each
// window. name prefixes every counter it keeps, so no two budgets may share
// one; message is the refusal's text.
func PerAddress(name string, limit int, window time.Duration, code Code, message string) AddressBudget {
	return AddressBudget{budget: declare(name, keyAddress, limit, window, code, message)}
}

// PerKey declares a budget per key the service resolved before charging it:
// an account, a registration, a contest. Otherwise as PerAddress.
func PerKey(name string, limit int, window time.Duration, code Code, message string) KeyBudget {
	return KeyBudget{budget: declare(name, keyResolved, limit, window, code, message)}
}

// PerTypedKey declares a budget per key the caller typed, such as an email or
// a login, which Charge spends only once every other budget of the call has
// admitted the request. Otherwise as PerAddress.
func PerTypedKey(name string, limit int, window time.Duration, code Code, message string) KeyBudget {
	return KeyBudget{budget: declare(name, keyTyped, limit, window, code, message)}
}

// Of is this budget charged to the request's client address, grouped as
// ClientSubject groups it.
func (b AddressBudget) Of(r *http.Request) Charge {
	subject := ClientSubject(r)
	if subject == "" {
		subject = "unknown"
	}
	return Charge{budget: b.budget, key: subject}
}

// Of is this budget charged to key.
func (b KeyBudget) Of(key string) Charge {
	return Charge{budget: b.budget, key: key}
}

// Charge is one budget and the subject it is charged to.
type Charge struct {
	budget budget
	key    string
}

// Refusal is a spent budget: the code and text it was declared with, and how
// long to wait.
type Refusal struct{ budget budget }

func (r *Refusal) Error() string { return "request budget " + r.budget.name + " spent" }

func (r *Refusal) Unwrap() error { return ErrBudgetSpent }

// Code is the code the budget was declared to refuse with.
func (r *Refusal) Code() Code { return r.budget.code }

// Message is the text the budget was declared to refuse with, for the client.
func (r *Refusal) Message() string { return r.budget.message }

// RetryAfter is the budget's whole window.
func (r *Refusal) RetryAfter() time.Duration { return r.budget.window }

// Budgets charges request budgets on one counter. A budget is declared once,
// as a value beside the routes that spend it (PerAddress, PerKey,
// PerTypedKey): a name, whom it counts, how many in what window, and the code
// its refusal answers with. Charge is the one place that spends them, so every
// route keeps these rules alike:
//
//   - CLAUDE.md rule 5: a call's budgets are charged address first, then the
//     keys the service resolved, then the keys the caller typed, and the first
//     refusal stops the call, so a refused caller makes no counter for a
//     string it invented;
//   - every refusal says to wait out its budget's window, the longest a caller
//     can have to wait, since a fixed window's start is not known here.
//
// The caller charges before the expensive step, and a refusal is charged like
// a success (rule 13).
type Budgets struct{ counter Counter }

// NewBudgets charges every budget on counter, which in a deployment is the
// shared cache's auth.Limiter, so a budget holds across replicas.
func NewBudgets(counter Counter) Budgets {
	return Budgets{counter: counter}
}

// chargeOrder is rule 5's order: the most bounded key first.
var chargeOrder = [...]keyKind{keyAddress, keyResolved, keyTyped}

// Charge spends one attempt of each budget, in rule 5's order whatever order
// they are named in, and stops at the first that refuses. It returns nil, a
// *Refusal, or an error wrapping ErrBudgetUnavailable.
func (b Budgets) Charge(ctx context.Context, charges ...Charge) error {
	for _, kind := range chargeOrder {
		for _, c := range charges {
			if c.budget.kind != kind {
				continue
			}
			allowed, err := b.counter.Allow(ctx, c.budget.name+":"+c.key, c.budget.limit, c.budget.window)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrBudgetUnavailable, err)
			}
			if !allowed {
				return &Refusal{budget: c.budget}
			}
		}
	}
	return nil
}
