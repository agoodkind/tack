package backfill

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"time"
)

func typeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name
	case *ast.Ident:
		return typed.Name
	default:
		return ""
	}
}

func checkLifetimeLiteral(literal *ast.CompositeLit, today time.Time) string {
	fields := map[string]ast.Expr{}

	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return "Lifetime must name its fields"
		}

		fields[typeName(pair.Key)] = pair.Value
	}

	ticket, validTicket := stringLiteral(fields["Ticket"])
	if !validTicket || ticket == "" {
		return "Lifetime.Ticket must be a string literal naming the ticket"
	}

	removeBy, validDate := dateLiteral(fields["RemoveBy"])
	if !validDate {
		return "Lifetime.RemoveBy must be a time.Date literal with a constant year, month, and day"
	}

	if today.After(removeBy) {
		return fmt.Sprintf(
			"backfill for %s was to be deleted by %s: delete the command and its tests",
			ticket,
			removeBy.Format(time.DateOnly),
		)
	}

	return ""
}

func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}

	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}

	return value, true
}

// dateLiteral reads a [time.Date] call with a literal year and day and a
// month written as a literal or as a [time.Month] constant. The removal day is
// the end of that calendar day in UTC.
func dateLiteral(expr ast.Expr) (time.Time, bool) {
	call, validCall := expr.(*ast.CallExpr)
	if !validCall || len(call.Args) < 3 || typeName(call.Fun) != "Date" {
		return time.Time{}, false
	}

	year, validYear := intLiteral(call.Args[0])
	if !validYear {
		return time.Time{}, false
	}

	month, validMonth := monthLiteral(call.Args[1])
	if !validMonth {
		return time.Time{}, false
	}

	day, validDay := intLiteral(call.Args[2])
	if !validDay {
		return time.Time{}, false
	}

	return time.Date(year, month, day, 23, 59, 59, 0, time.UTC), true
}

func intLiteral(expr ast.Expr) (int, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		return 0, false
	}

	value, err := strconv.Atoi(literal.Value)
	if err != nil {
		return 0, false
	}

	return value, true
}

func monthLiteral(expr ast.Expr) (time.Month, bool) {
	if value, ok := intLiteral(expr); ok {
		return time.Month(value), true
	}

	for month := time.January; month <= time.December; month++ {
		if typeName(expr) == month.String() {
			return month, true
		}
	}

	return 0, false
}
