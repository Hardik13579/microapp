package context

import (
	"strings"

	microappError "github.com/islax/microapp/error"
	"github.com/islax/microapp/log"
	"github.com/islax/microapp/repository"
	"github.com/islax/microapp/security"
	"github.com/rs/zerolog"
	uuid "github.com/satori/go.uuid"
)

// ExecutionContext execution context
type ExecutionContext interface {
	AddLoggerStrFields(strFields map[string]string)
	GetActionName() string
	GetCorrelationID() string
	GetDefaultLogger() *zerolog.Logger
	GetToken() *security.JwtToken
	GetUOW() *repository.UnitOfWork
	SetUOW(*repository.UnitOfWork)
	Logger(eventType, eventCode string) *zerolog.Logger
	LoggerEventActionCompletion() *zerolog.Event
	LogError(err error, errorMessage string)
	LogJSONParseError(err error)
	SubContext(additionalFields map[string]string) ExecutionContext
	SubContextWithToken(token *security.JwtToken, additionalFields map[string]string) ExecutionContext
	SubContextWithTokenAndUoW(token *security.JwtToken, uow *repository.UnitOfWork, additionalFields map[string]string) ExecutionContext
	SubContextWithUoW(uow *repository.UnitOfWork, additionalFields map[string]string) ExecutionContext
}

type executionContextImpl struct {
	CorrelationID string
	UOW           *repository.UnitOfWork
	Token         *security.JwtToken
	Action        string
	logger        zerolog.Logger
}

// NewExecutionContext creates new execution context
func NewExecutionContext(token *security.JwtToken, correlationID string, action string, logger zerolog.Logger) ExecutionContext {
	cid := correlationID
	if len(strings.TrimSpace(cid)) == 0 {
		cid = uuid.NewV4().String()
	}
	var executionCtxLogger zerolog.Logger
	if token != nil {
		executionCtxLogger = logger.With().
			Str("tenantId", token.TenantID.String()).
			Str("userId", token.UserID.String()).
			Str("username", token.UserName).
			Str("tenantName", token.TenantName).
			Str("userDisplayName", token.DisplayName).
			Str("action", action).
			Str("correlationId", cid).Logger()

	} else {
		executionCtxLogger = logger.With().
			Str("action", action).
			Str("correlationId", cid).Logger()
	}

	return &executionContextImpl{CorrelationID: cid, Token: token, Action: action, logger: executionCtxLogger}
}

// AddLoggerStrFields adds given string fields to the context logger
func (context *executionContextImpl) AddLoggerStrFields(strFields map[string]string) {
	loggerWith := context.logger.With()
	for k, v := range strFields {
		loggerWith = loggerWith.Str(k, v)
	}
	context.logger = loggerWith.Logger()
}

func (context *executionContextImpl) SubContextWithAddlFieldsAndUoW(uow *repository.UnitOfWork, additionalFields map[string]string) ExecutionContext {
	loggerWith := context.logger.With()
	for k, v := range additionalFields {
		loggerWith = loggerWith.Str(k, v)
	}
	return &executionContextImpl{context.CorrelationID, uow, context.Token, context.Action, loggerWith.Logger()}
}

func (context *executionContextImpl) GetActionName() string {
	return context.Action
}

func (context *executionContextImpl) GetCorrelationID() string {
	return context.CorrelationID
}

func (context *executionContextImpl) GetDefaultLogger() *zerolog.Logger {
	return &context.logger
}

func (context *executionContextImpl) GetToken() *security.JwtToken {
	return context.Token
}

func (context *executionContextImpl) GetUOW() *repository.UnitOfWork {
	return context.UOW
}

func (context *executionContextImpl) SetUOW(uow *repository.UnitOfWork) {
	context.UOW = uow
}

// Logger creates a logger with eventType and eventCode
func (context *executionContextImpl) Logger(eventType, eventCode string) *zerolog.Logger {
	logger := context.logger.With().Str("eventType", eventType).Str("eventCode", eventCode).Logger()
	return &logger
}

// LogError log error
func (context *executionContextImpl) LogError(err error, errorMessage string) {
	switch err.(type) {
	case microappError.ValidationError:
		context.Logger(log.EventTypeValidationErr, log.EventCodeInvalidData).Info().Err(err).Msg(log.MessageInvalidInputData)
	case microappError.HTTPResourceNotFound:
		resourceNotFoundErr := err.(microappError.HTTPResourceNotFound)
		context.Logger(log.EventTypeUnexpectedErr, resourceNotFoundErr.ErrorKey).Debug().Err(err).Str("resourceName", resourceNotFoundErr.ResourceName).Str("resourceValue", resourceNotFoundErr.ResourceValue).Msg(errorMessage)
	case microappError.APIClientError:
		apiCallError := err.(microappError.APIClientError)
		tmpLoggerEvent := context.Logger(log.EventTypeUnexpectedErr, apiCallError.GetErrorCode()).Error().Err(err).Str("stack", apiCallError.GetStackTrace()).Str("apiURL", apiCallError.GetAPIURL())
		if responseBody := apiCallError.GetHTTPResponseBody(); responseBody != nil {
			tmpLoggerEvent.Str("responseBody", *responseBody)
		}
		if responseCode := apiCallError.GetHTTPStatusCode(); responseCode != nil {
			tmpLoggerEvent = tmpLoggerEvent.Int("responseStatusCode", *responseCode)
		}
		tmpLoggerEvent.Msg(errorMessage)
	case microappError.UnexpectedError:
		context.Logger(log.EventTypeUnexpectedErr, err.(microappError.UnexpectedError).GetErrorCode()).Error().Err(err).Str("stack", err.(microappError.UnexpectedError).GetStackTrace()).Msg(errorMessage)
	default:
		context.Logger(log.EventTypeUnexpectedErr, log.EventCodeUnknown).Error().Err(err).Msg(errorMessage)
	}
}

// LogJSONParseError log JSON payload parsing error
func (context *executionContextImpl) LogJSONParseError(err error) {
	context.LogError(err, log.MessageParseError)
}

// LoggerEventActionCompletion logger event with eventType success and eventCode action complete
func (context *executionContextImpl) LoggerEventActionCompletion() *zerolog.Event {
	logger := context.logger.Info().Str("eventType", log.EventTypeSuccess).Str("eventCode", log.EventCodeActionComplete)
	return logger
}

func (context *executionContextImpl) SubContext(additionalFields map[string]string) ExecutionContext {
	loggerWith := context.logger.With()
	for k, v := range additionalFields {
		loggerWith = loggerWith.Str(k, v)
	}
	return &executionContextImpl{context.CorrelationID, context.UOW, context.Token, context.Action, loggerWith.Logger()}
}

func (context *executionContextImpl) SubContextWithToken(token *security.JwtToken, additionalFields map[string]string) ExecutionContext {
	loggerWith := context.logger.With()
	for k, v := range additionalFields {
		loggerWith = loggerWith.Str(k, v)
	}

	return &executionContextImpl{context.CorrelationID, context.UOW, token, context.Action, loggerWith.Logger()}
}

func (context *executionContextImpl) SubContextWithTokenAndUoW(token *security.JwtToken, uow *repository.UnitOfWork, additionalFields map[string]string) ExecutionContext {
	loggerWith := context.logger.With()
	for k, v := range additionalFields {
		loggerWith = loggerWith.Str(k, v)
	}

	return &executionContextImpl{context.CorrelationID, uow, token, context.Action, loggerWith.Logger()}
}

func (context *executionContextImpl) SubContextWithUoW(uow *repository.UnitOfWork, additionalFields map[string]string) ExecutionContext {
	loggerWith := context.logger.With()
	for k, v := range additionalFields {
		loggerWith = loggerWith.Str(k, v)
	}

	return &executionContextImpl{context.CorrelationID, uow, context.Token, context.Action, loggerWith.Logger()}
}
