package postgres

type rowScanner interface {
	Scan(...any) error
}
