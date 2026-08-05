package core

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// EvalExpression safely evaluates a simple arithmetic expression
// supporting + - * / % ^ parentheses, decimals and the functions
// sqrt, abs, round, floor, ceil, min, max, pow. It is a small
// recursive-descent parser — no eval, no code execution.
func EvalExpression(expr string) (float64, error) {
	p := &exprParser{tokens: tokenizeExpr(expr)}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	if p.pos < len(p.tokens) {
		return 0, fmt.Errorf("unexpected token %q", p.tokens[p.pos].val)
	}
	return v, nil
}

type exprToken struct {
	kind string // num | op | lparen | rparen | comma | ident
	val  string
	num  float64
}

func tokenizeExpr(s string) []exprToken {
	var out []exprToken
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		word := string(cur)
		cur = nil
		if n, err := strconv.ParseFloat(word, 64); err == nil {
			out = append(out, exprToken{kind: "num", num: n, val: word})
			return
		}
		if isIdent(word) {
			out = append(out, exprToken{kind: "ident", val: word})
			return
		}
		out = append(out, exprToken{kind: "op", val: word})
	}
	for _, r := range strings.TrimSpace(s) {
		switch {
		case unicode.IsSpace(r):
			flush()
		case r == '(':
			flush()
			out = append(out, exprToken{kind: "lparen", val: "("})
		case r == ')':
			flush()
			out = append(out, exprToken{kind: "rparen", val: ")"})
		case r == ',':
			flush()
			out = append(out, exprToken{kind: "comma", val: ","})
		case r == '+' || r == '-' || r == '*' || r == '/' || r == '%' || r == '^':
			flush()
			out = append(out, exprToken{kind: "op", val: string(r)})
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && !unicode.IsLetter(r) {
			return false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

type exprParser struct {
	tokens []exprToken
	pos    int
}

func (p *exprParser) peek() *exprToken {
	if p.pos >= len(p.tokens) {
		return nil
	}
	return &p.tokens[p.pos]
}

func (p *exprParser) next() *exprToken {
	t := p.peek()
	if t != nil {
		p.pos++
	}
	return t
}

// parseExpr handles + and - (lowest precedence).
func (p *exprParser) parseExpr() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != "op" || (t.val != "+" && t.val != "-") {
			return left, nil
		}
		p.next()
		right, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if t.val == "+" {
			left += right
		} else {
			left -= right
		}
	}
}

// parseTerm handles * / %.
func (p *exprParser) parseTerm() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != "op" || (t.val != "*" && t.val != "/" && t.val != "%") {
			return left, nil
		}
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		switch t.val {
		case "*":
			left *= right
		case "/":
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		case "%":
			if right == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			left = math.Mod(left, right)
		}
	}
}

// parseUnary handles unary minus and exponentiation. Unary plus is
// deliberately rejected so expressions like "2 ++ 3" fail fast.
func (p *exprParser) parseUnary() (float64, error) {
	t := p.peek()
	if t != nil && t.kind == "op" && t.val == "-" {
		p.next()
		v, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return -v, nil
	}
	base, err := p.parsePower()
	if err != nil {
		return 0, err
	}
	if t := p.peek(); t != nil && t.kind == "op" && t.val == "^" {
		p.next()
		exp, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return math.Pow(base, exp), nil
	}
	return base, nil
}

// parsePower handles the atom (number, parens, function call).
func (p *exprParser) parsePower() (float64, error) {
	t := p.next()
	if t == nil {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	switch t.kind {
	case "num":
		return t.num, nil
	case "lparen":
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if rt := p.next(); rt == nil || rt.kind != "rparen" {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		return v, nil
	case "ident":
		args := []float64{}
		if pt := p.peek(); pt != nil && pt.kind == "lparen" {
			p.next()
			if ct := p.peek(); ct != nil && ct.kind == "rparen" {
				p.next()
			} else {
				for {
					a, err := p.parseExpr()
					if err != nil {
						return 0, err
					}
					args = append(args, a)
					sep := p.next()
					if sep == nil {
						return 0, fmt.Errorf("unterminated function call")
					}
					if sep.kind == "rparen" {
						break
					}
					if sep.kind != "comma" {
						return 0, fmt.Errorf("expected ',' or ')' in call to %s", t.val)
					}
				}
			}
		}
		return callFunction(t.val, args)
	}
	return 0, fmt.Errorf("unexpected token %q", t.val)
}

func callFunction(name string, args []float64) (float64, error) {
	arity := func(n int) error {
		if len(args) != n {
			return fmt.Errorf("%s expects %d argument(s), got %d", name, n, len(args))
		}
		return nil
	}
	switch name {
	case "sqrt":
		if err := arity(1); err != nil {
			return 0, err
		}
		if args[0] < 0 {
			return 0, fmt.Errorf("sqrt of negative number")
		}
		return math.Sqrt(args[0]), nil
	case "abs":
		if err := arity(1); err != nil {
			return 0, err
		}
		return math.Abs(args[0]), nil
	case "round":
		if err := arity(1); err != nil {
			return 0, err
		}
		return math.Round(args[0]), nil
	case "floor":
		if err := arity(1); err != nil {
			return 0, err
		}
		return math.Floor(args[0]), nil
	case "ceil":
		if err := arity(1); err != nil {
			return 0, err
		}
		return math.Ceil(args[0]), nil
	case "pow":
		if err := arity(2); err != nil {
			return 0, err
		}
		return math.Pow(args[0], args[1]), nil
	case "min":
		if len(args) == 0 {
			return 0, fmt.Errorf("min expects at least 1 argument")
		}
		m := args[0]
		for _, a := range args[1:] {
			if a < m {
				m = a
			}
		}
		return m, nil
	case "max":
		if len(args) == 0 {
			return 0, fmt.Errorf("max expects at least 1 argument")
		}
		m := args[0]
		for _, a := range args[1:] {
			if a > m {
				m = a
			}
		}
		return m, nil
	}
	return 0, fmt.Errorf("unknown function %q", name)
}
