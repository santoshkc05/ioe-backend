package app

import "errors"

var (
	ErrNotFound               = errors.New("not found")
	ErrCourseFree             = errors.New("course is free")
	ErrAlreadyEnrolled        = errors.New("already enrolled")
	ErrAlreadyPurchased       = errors.New("already purchased")
	ErrGatewayUnavailable     = errors.New("payment gateway unavailable")
	ErrConcurrentModification = errors.New("concurrent modification")
	ErrInvalidInput           = errors.New("invalid input")
	ErrForbidden              = errors.New("forbidden")
	ErrNotRefundable          = errors.New("purchase is not refundable")
)
