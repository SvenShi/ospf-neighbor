package ospf_cnn

import (
	"fmt"
	"github.com/sirupsen/logrus"
)

// 创建一个全局日志实例
var logger = logrus.New()

// LogDebug 实现调试级别的日志
func LogDebug(format string, args ...interface{}) {
	// 打印 debug 级别的日志
	logger.Debug(fmt.Sprintf(format, args...))
}

// LogWarn 实现警告级别的日志
func LogWarn(format string, args ...interface{}) {
	// 打印 warn 级别的日志
	logger.Warn(fmt.Sprintf(format, args...))
}

// LogErr 实现错误级别的日志
func LogErr(format string, args ...interface{}) {
	// 打印 error 级别的日志
	logger.Error(fmt.Sprintf(format, args...))
}

// LogInfo 实现重要信息级别的日志
func LogInfo(format string, args ...interface{}) {
	// 打印 info 级别的日志
	logger.Info(fmt.Sprintf(format, args...))
}

// 初始化函数设置日志格式和日志级别
func init() {
	// 设置日志格式为文本格式（也可以选择 JSON 格式）
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	// 设置日志级别为 Debug，这样所有级别的日志都会输出
	logger.SetLevel(logrus.InfoLevel)
}
