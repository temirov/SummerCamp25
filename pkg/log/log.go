package log

import "go.uber.org/zap"

var logger *zap.Logger

func Init(prod bool) error {
	if logger != nil {
		return nil
	}
	var err error
	if prod {
		logger, err = zap.NewProduction()
	} else {
		logger, err = zap.NewDevelopment()
	}
	return err
}

func L() *zap.Logger {
	if logger == nil {
		panic("logger not initialized")
	}
	return logger
}
