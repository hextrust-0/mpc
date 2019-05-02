//
// lexer.go
//
// Copyright (c) 2019 Markku Rossi
//
// All rights reserved.
//

package compiler

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"unicode"
)

type TokenType int

const (
	T_Identifier TokenType = iota
	T_Symbol
	T_SymFunc
	T_SymReturn
	T_Type
	T_Div
	T_DivEq
	T_Plus
	T_PlusPlus
	T_PlusEq
	T_LParen
	T_RParen
	T_LBrace
	T_RBrace
	T_Comma
)

var tokenTypes = map[TokenType]string{
	T_Identifier: "identifier",
	T_Symbol:     "symbol",
	T_SymFunc:    "func",
	T_SymReturn:  "return",
	T_Div:        "/",
	T_DivEq:      "/=",
	T_Plus:       "+",
	T_PlusPlus:   "++",
	T_PlusEq:     "+=",
	T_LParen:     "(",
	T_RParen:     ")",
	T_LBrace:     "{",
	T_RBrace:     "}",
	T_Comma:      ",",
}

func (t TokenType) String() string {
	name, ok := tokenTypes[t]
	if ok {
		return name
	}
	return fmt.Sprintf("{TokenType %d}", t)
}

var symbols = map[string]TokenType{
	"func":   T_SymFunc,
	"return": T_SymReturn,
}

var reType = regexp.MustCompilePOSIX(`^(int|float)([[:digit:]]*)$`)

type Type int

const (
	TypeInt Type = iota
	TypeFloat
)

var types = map[string]Type{
	"int":   TypeInt,
	"float": TypeFloat,
}

type Token struct {
	Type     TokenType
	From     Point
	To       Point
	StrVal   string
	TypeType Type
	TypeBits int
}

func (t *Token) String() string {
	var str string
	if len(t.StrVal) > 0 {
		str = t.StrVal
	} else {
		str = t.Type.String()
	}
	return fmt.Sprintf("%s [%s-%s]", str, t.From, t.To)
}

type Point struct {
	Line int // 1-based
	Col  int // 0-based
}

func (s Point) String() string {
	return fmt.Sprintf("%d:%d", s.Line, s.Col)
}

type Lexer struct {
	in          *bufio.Reader
	point       Point
	tokenStart  Point
	ungot       *Token
	unread      bool
	unreadRune  rune
	unreadPoint Point
}

func NewLexer(in io.Reader) *Lexer {
	return &Lexer{
		in: bufio.NewReader(in),
		point: Point{
			Line: 1,
			Col:  0,
		},
	}
}

func (l *Lexer) ReadRune() (rune, error) {
	if l.unread {
		l.point, l.unreadPoint = l.unreadPoint, l.point
		l.unread = false
		return l.unreadRune, nil
	}
	r, _, err := l.in.ReadRune()
	if err != nil {
		return 0, err
	}

	l.unreadPoint = l.point
	if r == '\n' {
		l.point.Line++
		l.point.Col = 0
	} else {
		l.point.Col++
	}

	return r, nil
}

func (l *Lexer) UnreadRune(r rune) {
	l.point, l.unreadPoint = l.unreadPoint, l.point
	l.unreadRune = r
	l.unread = true
}

func (l *Lexer) Get() (*Token, error) {
	if l.ungot != nil {
		token := l.ungot
		l.ungot = nil
		return token, nil
	}

	for {
		l.tokenStart = l.point
		r, err := l.ReadRune()
		if err != nil {
			return nil, err
		}
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '+':
			r, err := l.ReadRune()
			if err != nil {
				if err == io.EOF {
					return l.Token(T_Plus), nil
				}
				return nil, err
			}
			switch r {
			case '+':
				return l.Token(T_PlusPlus), nil
			case '=':
				return l.Token(T_PlusEq), nil
			default:
				l.UnreadRune(r)
				return l.Token(T_Plus), nil
			}

		case '/':
			r, err := l.ReadRune()
			if err != nil {
				if err == io.EOF {
					return l.Token(T_Div), nil
				}
				return nil, err
			}
			switch r {
			case '/':
				for {
					r, err := l.ReadRune()
					if err != nil {
						return nil, err
					}
					if r == '\n' {
						break
					}
				}
				continue

			case '=':
				return l.Token(T_DivEq), nil

			default:
				l.UnreadRune(r)
				return l.Token(T_Div), nil
			}

		case '(':
			return l.Token(T_LParen), nil
		case ')':
			return l.Token(T_RParen), nil
		case '{':
			return l.Token(T_LBrace), nil
		case '}':
			return l.Token(T_RBrace), nil
		case ',':
			return l.Token(T_Comma), nil

		default:
			if unicode.IsLetter(r) {
				symbol := string(r)
				for {
					r, err := l.ReadRune()
					if err != nil {
						if err != io.EOF {
							return nil, err
						}
						break
					}
					if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
						l.UnreadRune(r)
						break
					}
					symbol += string(r)
				}
				tt, ok := symbols[symbol]
				if ok {
					return l.Token(tt), nil
				}
				matches := reType.FindStringSubmatch(symbol)
				if matches != nil {
					tt, ok := types[matches[1]]
					if ok {
						token := l.Token(T_Type)
						token.TypeType = tt
						ival, _ := strconv.Atoi(matches[2])
						token.TypeBits = ival
						token.StrVal = symbol
						return token, nil
					}
				}

				token := l.Token(T_Identifier)
				token.StrVal = symbol
				return token, nil
			}
			l.UnreadRune(r)
			return nil, fmt.Errorf("%s: unexpected character '%s'",
				l.point, string(r))
		}
	}
}

func (l *Lexer) Unget(t *Token) {
	l.ungot = t
}

func (l *Lexer) Token(t TokenType) *Token {
	return &Token{
		Type: t,
		From: l.tokenStart,
		To:   l.point,
	}
}
