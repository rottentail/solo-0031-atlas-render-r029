package model

import "fmt"

type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *Error) Error() string { return e.Message }
func Invalid(format string, args ...any) error {
	return &Error{400, "invalid", fmt.Sprintf(format, args...), nil}
}
func Conflict(message string) error { return &Error{409, "conflict", message, nil} }
func ConflictDetails(message string, details any) error {
	return &Error{409, "conflict", message, details}
}
func Missing(message string) error { return &Error{404, "missing", message, nil} }
func Gone(message string) error    { return &Error{410, "gone", message, nil} }
func Busy(message string) error    { return &Error{429, "capacity", message, nil} }
