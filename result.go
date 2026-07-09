// Package gominiaudio is a native Go implementation of the miniaudio audio
// library (https://github.com/mackron/miniaudio). It provides device
// playback/capture, data conversion, decoding, filters, a node graph and a
// high-level engine without cgo. Platform backends: WASAPI on Windows,
// PipeWire on Linux, CoreAudio on macOS.
//
// It depends only on the Go standard library and golang.org/x packages.
package gominiaudio

import "fmt"

// Result mirrors ma_result. The zero value is Success. Result implements
// error; Success is represented by a nil error in idiomatic Go APIs, but the
// numeric codes are preserved for parity with miniaudio.
type Result int32

// Result codes, mirroring ma_result in miniaudio.h.
const (
	Success                       Result = 0
	ErrorGeneric                  Result = -1
	ErrInvalidArgs                Result = -2
	ErrInvalidOperation           Result = -3
	ErrOutOfMemory                Result = -4
	ErrOutOfRange                 Result = -5
	ErrAccessDenied               Result = -6
	ErrDoesNotExist               Result = -7
	ErrAlreadyExists              Result = -8
	ErrTooManyOpenFiles           Result = -9
	ErrInvalidFile                Result = -10
	ErrTooBig                     Result = -11
	ErrPathTooLong                Result = -12
	ErrNameTooLong                Result = -13
	ErrNotDirectory               Result = -14
	ErrIsDirectory                Result = -15
	ErrDirectoryNotEmpty          Result = -16
	ErrAtEnd                      Result = -17
	ErrNoSpace                    Result = -18
	ErrBusy                       Result = -19
	ErrIOError                    Result = -20
	ErrInterrupt                  Result = -21
	ErrUnavailable                Result = -22
	ErrAlreadyInUse               Result = -23
	ErrBadAddress                 Result = -24
	ErrBadSeek                    Result = -25
	ErrBadPipe                    Result = -26
	ErrDeadlock                   Result = -27
	ErrTooManyLinks               Result = -28
	ErrNotImplemented             Result = -29
	ErrNoMessage                  Result = -30
	ErrBadMessage                 Result = -31
	ErrNoDataAvailable            Result = -32
	ErrInvalidData                Result = -33
	ErrTimeout                    Result = -34
	ErrNoNetwork                  Result = -35
	ErrNotUnique                  Result = -36
	ErrNotSocket                  Result = -37
	ErrNoAddress                  Result = -38
	ErrBadProtocol                Result = -39
	ErrProtocolUnavailable        Result = -40
	ErrProtocolNotSupported       Result = -41
	ErrProtocolFamilyNotSupported Result = -42
	ErrAddressFamilyNotSupported  Result = -43
	ErrSocketNotSupported         Result = -44
	ErrConnectionReset            Result = -45
	ErrAlreadyConnected           Result = -46
	ErrNotConnected               Result = -47
	ErrConnectionRefused          Result = -48
	ErrNoHost                     Result = -49
	ErrInProgress                 Result = -50
	ErrCancelled                  Result = -51
	ErrMemoryAlreadyMapped        Result = -52

	ErrCRCMismatch Result = -100

	ErrFormatNotSupported     Result = -200
	ErrDeviceTypeNotSupported Result = -201
	ErrShareModeNotSupported  Result = -202
	ErrNoBackend              Result = -203
	ErrNoDevice               Result = -204
	ErrAPINotFound            Result = -205
	ErrInvalidDeviceConfig    Result = -206
	ErrLoop                   Result = -207
	ErrBackendNotEnabled      Result = -208

	ErrDeviceNotInitialized     Result = -300
	ErrDeviceAlreadyInitialized Result = -301
	ErrDeviceNotStarted         Result = -302
	ErrDeviceNotStopped         Result = -303

	ErrFailedToInitBackend        Result = -400
	ErrFailedToOpenBackendDevice  Result = -401
	ErrFailedToStartBackendDevice Result = -402
	ErrFailedToStopBackendDevice  Result = -403
)

var resultStrings = map[Result]string{
	Success:                       "No error",
	ErrorGeneric:                  "Error",
	ErrInvalidArgs:                "Invalid args",
	ErrInvalidOperation:           "Invalid operation",
	ErrOutOfMemory:                "Out of memory",
	ErrOutOfRange:                 "Out of range",
	ErrAccessDenied:               "Permission denied",
	ErrDoesNotExist:               "Resource does not exist",
	ErrAlreadyExists:              "Resource already exists",
	ErrTooManyOpenFiles:           "Too many open files",
	ErrInvalidFile:                "Invalid file",
	ErrTooBig:                     "Too large",
	ErrPathTooLong:                "Path too long",
	ErrNameTooLong:                "Name too long",
	ErrNotDirectory:               "Not a directory",
	ErrIsDirectory:                "Is a directory",
	ErrDirectoryNotEmpty:          "Directory not empty",
	ErrAtEnd:                      "At end",
	ErrNoSpace:                    "No space available",
	ErrBusy:                       "Device or resource busy",
	ErrIOError:                    "Input/output error",
	ErrInterrupt:                  "Interrupted",
	ErrUnavailable:                "Resource unavailable",
	ErrAlreadyInUse:               "Resource already in use",
	ErrBadAddress:                 "Bad address",
	ErrBadSeek:                    "Illegal seek",
	ErrBadPipe:                    "Broken pipe",
	ErrDeadlock:                   "Deadlock",
	ErrTooManyLinks:               "Too many links",
	ErrNotImplemented:             "Not implemented",
	ErrNoMessage:                  "No message of desired type",
	ErrBadMessage:                 "Invalid message",
	ErrNoDataAvailable:            "No data available",
	ErrInvalidData:                "Invalid data",
	ErrTimeout:                    "Timeout",
	ErrNoNetwork:                  "Network unavailable",
	ErrNotUnique:                  "Not unique",
	ErrNotSocket:                  "Socket operation on non-socket",
	ErrNoAddress:                  "Destination address required",
	ErrBadProtocol:                "Protocol wrong type for socket",
	ErrProtocolUnavailable:        "Protocol not available",
	ErrProtocolNotSupported:       "Protocol not supported",
	ErrProtocolFamilyNotSupported: "Protocol family not supported",
	ErrAddressFamilyNotSupported:  "Address family not supported",
	ErrSocketNotSupported:         "Socket type not supported",
	ErrConnectionReset:            "Connection reset",
	ErrAlreadyConnected:           "Already connected",
	ErrNotConnected:               "Not connected",
	ErrConnectionRefused:          "Connection refused",
	ErrNoHost:                     "No host",
	ErrInProgress:                 "Operation in progress",
	ErrCancelled:                  "Operation cancelled",
	ErrMemoryAlreadyMapped:        "Memory already mapped",
	ErrCRCMismatch:                "CRC mismatch",
	ErrFormatNotSupported:         "Format not supported",
	ErrDeviceTypeNotSupported:     "Device type not supported",
	ErrShareModeNotSupported:      "Share mode not supported",
	ErrNoBackend:                  "No backend",
	ErrNoDevice:                   "No device",
	ErrAPINotFound:                "API not found",
	ErrInvalidDeviceConfig:        "Invalid device config",
	ErrLoop:                       "Loop",
	ErrBackendNotEnabled:          "Backend not enabled",
	ErrDeviceNotInitialized:       "Device not initialized",
	ErrDeviceAlreadyInitialized:   "Device already initialized",
	ErrDeviceNotStarted:           "Device not started",
	ErrDeviceNotStopped:           "Device not stopped",
	ErrFailedToInitBackend:        "Failed to initialize backend",
	ErrFailedToOpenBackendDevice:  "Failed to open backend device",
	ErrFailedToStartBackendDevice: "Failed to start backend device",
	ErrFailedToStopBackendDevice:  "Failed to stop backend device",
}

// Error implements the error interface. Mirrors ma_result_description.
func (r Result) Error() string {
	if s, ok := resultStrings[r]; ok {
		return s
	}
	return fmt.Sprintf("Unknown error (%d)", int32(r))
}

// Description mirrors ma_result_description.
func (r Result) Description() string { return r.Error() }

// errOrNil converts a Result into an error, mapping Success to nil.
func (r Result) errOrNil() error {
	if r == Success {
		return nil
	}
	return r
}
