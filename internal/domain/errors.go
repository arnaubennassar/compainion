package domain

import "fmt"

type Kind int

const (
	NotFound Kind = iota + 1
	Conflict
	Invalid
	Unprocessable
	PreconditionFailed
	PreconditionRequired
)

type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func Errf(k Kind, f string, a ...any) *Error { return &Error{Kind: k, Msg: fmt.Sprintf(f, a...)} }
