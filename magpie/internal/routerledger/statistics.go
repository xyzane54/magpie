package routerledger

import (
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// TokenAggregate is the known subtotal, with coverage alongside it. A missing
// subtotal means no value was reported; an explicitly reported zero stays zero.
type TokenAggregate struct {
	KnownTokens     *int64 `json:"knownTokens"`
	KnownRequests   int    `json:"knownRequests"`
	UnknownRequests int    `json:"unknownRequests"`
}

type DestinationStatistics struct {
	Destination  string         `json:"destination"`
	Total        int            `json:"total"`
	Share        *float64       `json:"share"`
	Processing   int            `json:"processing"`
	Succeeded    int            `json:"succeeded"`
	Failed       int            `json:"failed"`
	Canceled     int            `json:"canceled"`
	TimedOut     int            `json:"timedOut"`
	Interrupted  int            `json:"interrupted"`
	InputTokens  TokenAggregate `json:"inputTokens"`
	OutputTokens TokenAggregate `json:"outputTokens"`
	TotalTokens  TokenAggregate `json:"totalTokens"`
}

// MoneyStatistics keeps currencies separate and amounts as exact decimal text.
// These are estimates from immutable user reference prices, never billed cost.
type MoneyStatistics struct {
	Currency        string  `json:"currency"`
	Unit            string  `json:"unit"`
	KnownAmount     *string `json:"knownAmount"`
	KnownRequests   int     `json:"knownRequests"`
	UnknownRequests int     `json:"unknownRequests"`
}

type SavingsStatistics struct {
	EligibleRequests     int               `json:"eligibleRequests"`
	InputTokens          TokenAggregate    `json:"inputTokens"`
	OutputTokens         TokenAggregate    `json:"outputTokens"`
	AvoidedTokens        TokenAggregate    `json:"avoidedTokens"`
	Money                []MoneyStatistics `json:"money"`
	UnknownPriceRequests int               `json:"unknownPriceRequests"`
}

type TimeAggregate struct {
	KnownMS      *int64 `json:"knownMs"`
	KnownCalls   int    `json:"knownCalls"`
	UnknownCalls int    `json:"unknownCalls"`
}

type TimingStatistics struct {
	QueueLoadMS      TimeAggregate `json:"queueLoadMs"`
	QueueMS          TimeAggregate `json:"queueMs"`
	LoadMS           TimeAggregate `json:"loadMs"`
	InferenceMS      TimeAggregate `json:"inferenceMs"`
	RequestHeadersMS TimeAggregate `json:"requestHeadersMs"`
	ConsumeMS        TimeAggregate `json:"consumeMs"`
	TotalMS          TimeAggregate `json:"totalMs"`
}

type CallStatistics struct {
	Purpose              string           `json:"purpose"`
	Total                int              `json:"total"`
	InferenceStarted     int              `json:"inferenceStarted"`
	NotStarted           int              `json:"notStarted"`
	Results              map[string]int   `json:"results"`
	InputTokens          TokenAggregate   `json:"inputTokens"`
	OutputTokens         TokenAggregate   `json:"outputTokens"`
	TotalTokens          TokenAggregate   `json:"totalTokens"`
	Timings              TimingStatistics `json:"timings"`
	BusinessUsageCalls   int              `json:"businessUsageCalls"`
	OverheadUsageCalls   int              `json:"overheadUsageCalls"`
	OverheadInputTokens  TokenAggregate   `json:"overheadInputTokens"`
	OverheadOutputTokens TokenAggregate   `json:"overheadOutputTokens"`
	OverheadTotalTokens  TokenAggregate   `json:"overheadTotalTokens"`
}

// CoreStatistics deliberately has no business-plus-internal usage total. One
// successful local execution can own the business result as well as a call row.
type CoreStatistics struct {
	Destinations  []DestinationStatistics `json:"destinations"`
	Savings       SavingsStatistics       `json:"savings"`
	InternalCalls []CallStatistics        `json:"internalCalls"`
	CheckCalls    []CallStatistics        `json:"checkCalls"`
}

func addKnown(sum **int64, value int64) error {
	if value < 0 || *sum != nil && value > math.MaxInt64-**sum {
		return ErrUnavailable
	}
	if *sum == nil {
		*sum = new(int64)
	}
	**sum += value
	return nil
}

func (a *TokenAggregate) add(value *int64) error {
	if value == nil {
		a.UnknownRequests++
		return nil
	}
	if err := addKnown(&a.KnownTokens, *value); err != nil {
		return err
	}
	a.KnownRequests++
	return nil
}

func addUsage(input, output, total *TokenAggregate, usage *Usage) error {
	if usage == nil {
		usage = &Usage{}
	}
	for i, value := range []*int64{usage.InputTokens, usage.OutputTokens, usage.TotalTokens} {
		if err := []*TokenAggregate{input, output, total}[i].add(value); err != nil {
			return err
		}
	}
	return nil
}

func (a *TimeAggregate) add(value *int64) error {
	if value == nil {
		a.UnknownCalls++
		return nil
	}
	if err := addKnown(&a.KnownMS, *value); err != nil {
		return err
	}
	a.KnownCalls++
	return nil
}

func (a *TimingStatistics) add(t CallTimings) error {
	for i, value := range []*int64{t.QueueLoadMS, t.QueueMS, t.LoadMS, t.InferenceMS, t.RequestHeadersMS, t.ConsumeMS, t.TotalMS} {
		if err := []*TimeAggregate{&a.QueueLoadMS, &a.QueueMS, &a.LoadMS, &a.InferenceMS, &a.RequestHeadersMS, &a.ConsumeMS, &a.TotalMS}[i].add(value); err != nil {
			return err
		}
	}
	return nil
}

func (d *DestinationStatistics) add(r Record) error {
	d.Total++
	switch r.Status {
	case "processing":
		d.Processing++
	case "succeeded":
		d.Succeeded++
	case "failed":
		d.Failed++
	case "canceled":
		d.Canceled++
	case "timed_out":
		d.TimedOut++
	case "interrupted":
		d.Interrupted++
	}
	return addUsage(&d.InputTokens, &d.OutputTokens, &d.TotalTokens, r.Usage)
}

type moneyAccumulator struct {
	statistics MoneyStatistics
	amount     *big.Rat
}

// decimalAmount returns an exact finite decimal. Price floats are first read as
// their shortest decimal representation, matching the persisted JSON snapshot.
func decimalAmount(r *big.Rat) string {
	denominator := new(big.Int).Set(r.Denom())
	var twos, fives int
	for denominator.Bit(0) == 0 {
		denominator.Rsh(denominator, 1)
		twos++
	}
	five := big.NewInt(5)
	for new(big.Int).Mod(denominator, five).Sign() == 0 {
		denominator.Quo(denominator, five)
		fives++
	}
	// Reference prices are finite decimal numbers and the unit is a million
	// tokens, so their denominator contains only factors of two and five.
	scale := max(twos, fives)
	value := r.FloatString(scale)
	if strings.Contains(value, ".") {
		value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	return value
}

func referenceAmount(price *ReferencePrice, usage *Usage) *big.Rat {
	if price == nil || price.Unit != "million-tokens" || usage == nil || usage.InputTokens == nil || usage.OutputTokens == nil {
		return nil
	}
	input, ok := new(big.Rat).SetString(strconv.FormatFloat(price.Input, 'g', -1, 64))
	if !ok {
		return nil
	}
	output, ok := new(big.Rat).SetString(strconv.FormatFloat(price.Output, 'g', -1, 64))
	if !ok {
		return nil
	}
	input.Mul(input, new(big.Rat).SetInt64(*usage.InputTokens))
	output.Mul(output, new(big.Rat).SetInt64(*usage.OutputTokens))
	return input.Add(input, output).Quo(input, big.NewRat(1000000, 1))
}

func emptyCallStatistics() []CallStatistics {
	groups := []CallStatistics{}
	for _, purpose := range []string{"judgment", "execution", "redaction"} {
		groups = append(groups, CallStatistics{Purpose: purpose, Results: map[string]int{}})
	}
	return groups
}

func usageEqual(a, b *Usage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	for i, p := range []*int64{a.InputTokens, a.OutputTokens, a.TotalTokens} {
		q := []*int64{b.InputTokens, b.OutputTokens, b.TotalTokens}[i]
		if p == nil != (q == nil) || p != nil && *p != *q {
			return false
		}
	}
	return true
}

// businessExecutions picks at most one matching execution for each local result.
// Failed local attempts before a cloud fallback remain internal overhead.
func businessExecutions(records map[string]Record, calls []Call) map[string]bool {
	selected := map[string]Call{}
	for _, call := range calls {
		r, ok := records[call.RequestID]
		if !ok || r.Operation != "generate" || r.Destination != "local" || call.Scope != "business" || call.Purpose != "execution" || !call.InferenceStarted {
			continue
		}
		if r.LocalCallID != "" {
			if r.LocalCallID != call.CallID {
				continue
			}
		} else if !usageEqual(r.Usage, call.Usage) || r.LocalModel != "" && r.LocalModel != call.ModelID {
			continue
		}
		previous, exists := selected[call.RequestID]
		if !exists || call.EndedAt.After(previous.EndedAt) || call.EndedAt.Equal(previous.EndedAt) && call.CallID > previous.CallID {
			selected[call.RequestID] = call
		}
	}
	out := map[string]bool{}
	for _, call := range selected {
		out[call.CallID] = true
	}
	return out
}

func aggregateCalls(groups []CallStatistics, calls []Call, business map[string]bool) error {
	for _, call := range calls {
		for i := range groups {
			group := &groups[i]
			if group.Purpose != call.Purpose {
				continue
			}
			group.Total++
			group.Results[call.Result]++
			if err := group.Timings.add(call.Timings); err != nil {
				return err
			}
			if !call.InferenceStarted {
				group.NotStarted++
				continue
			}
			group.InferenceStarted++
			if err := addUsage(&group.InputTokens, &group.OutputTokens, &group.TotalTokens, call.Usage); err != nil {
				return err
			}
			if business[call.CallID] {
				group.BusinessUsageCalls++
				continue
			}
			group.OverheadUsageCalls++
			if err := addUsage(&group.OverheadInputTokens, &group.OverheadOutputTokens, &group.OverheadTotalTokens, call.Usage); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildCoreStatistics(records []Record, calls, checks []Call) (CoreStatistics, error) {
	out := CoreStatistics{Savings: SavingsStatistics{Money: []MoneyStatistics{}}, InternalCalls: emptyCallStatistics(), CheckCalls: emptyCallStatistics()}
	for _, destination := range []string{"local", "cloud", "none", "unknown"} {
		out.Destinations = append(out.Destinations, DestinationStatistics{Destination: destination})
	}
	indexed := map[string]Record{}
	money := map[string]*moneyAccumulator{}
	for _, record := range records {
		indexed[record.RequestID] = record
		destination := record.Destination
		if destination == "" {
			destination = "unknown"
		}
		for i := range out.Destinations {
			if out.Destinations[i].Destination == destination {
				if err := out.Destinations[i].add(record); err != nil {
					return CoreStatistics{}, err
				}
			}
		}
		if record.Operation != "generate" || record.Status != "succeeded" || record.Destination != "local" {
			continue
		}
		savings := &out.Savings
		savings.EligibleRequests++
		usage := record.Usage
		if usage == nil {
			usage = &Usage{}
		}
		if err := savings.InputTokens.add(usage.InputTokens); err != nil {
			return CoreStatistics{}, err
		}
		if err := savings.OutputTokens.add(usage.OutputTokens); err != nil {
			return CoreStatistics{}, err
		}
		var avoided *int64
		if usage.InputTokens != nil && usage.OutputTokens != nil {
			if *usage.InputTokens > math.MaxInt64-*usage.OutputTokens {
				return CoreStatistics{}, ErrUnavailable
			}
			value := *usage.InputTokens + *usage.OutputTokens
			avoided = &value
		}
		if err := savings.AvoidedTokens.add(avoided); err != nil {
			return CoreStatistics{}, err
		}
		price := record.ReferencePrice
		if price == nil {
			savings.UnknownPriceRequests++
			continue
		}
		group := money[price.Currency]
		if group == nil {
			group = &moneyAccumulator{statistics: MoneyStatistics{Currency: price.Currency, Unit: "million-tokens"}, amount: new(big.Rat)}
			money[price.Currency] = group
		}
		amount := referenceAmount(price, usage)
		if amount == nil {
			group.statistics.UnknownRequests++
		} else {
			group.statistics.KnownRequests++
			group.amount.Add(group.amount, amount)
		}
	}
	for i := range out.Destinations {
		if len(records) > 0 {
			share := float64(out.Destinations[i].Total) / float64(len(records))
			out.Destinations[i].Share = &share
		}
	}
	currencies := []string{}
	for currency := range money {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	for _, currency := range currencies {
		group := money[currency]
		if group.statistics.KnownRequests > 0 {
			amount := decimalAmount(group.amount)
			group.statistics.KnownAmount = &amount
		}
		out.Savings.Money = append(out.Savings.Money, group.statistics)
	}
	if err := aggregateCalls(out.InternalCalls, calls, businessExecutions(indexed, calls)); err != nil {
		return CoreStatistics{}, err
	}
	if err := aggregateCalls(out.CheckCalls, checks, nil); err != nil {
		return CoreStatistics{}, err
	}
	return out, nil
}
