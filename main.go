package main

import (
	"flag"
	"fmt"
	"github.com/SvenShi/ospf-neighbor/ospf_cnn"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"text/template"
)

const serviceTemplate = `[Unit]
Description=OSPF Neighbor Service
After=network.target

[Service]
ExecStart={{.ExecPath}} {{.IfaceFlag}} {{.IpFlag}} {{.DestroyFlag}}
Restart=always
User=root

[Install]
WantedBy=multi-user.target
`

func main() {
	// 获取第一个非标志参数，检查是否为 install 或 uninstall 命令
	args := os.Args[1:]

	var command string

	// 如果第一个参数是 install 或 uninstall，移除它并处理
	if len(args) > 0 && (args[0] == "install" || args[0] == "uninstall") {
		command = args[0]
		// 移除第一个参数
		args = args[1:]
	}

	// 定义命令行参数
	var iface string
	var ip string
	var destroy bool // 用于控制是否在应用退出时关闭路由器

	// 使用flag包来定义参数
	flag.StringVar(&iface, "iface", "", "Network interface name")
	flag.StringVar(&ip, "ip", "", "IP address with CIDR (e.g., 192.168.1.1/24)")
	flag.BoolVar(&destroy, "destroy", false, "If true, destroy the router on exit")

	flag.CommandLine.Parse(args)
	// 如果命令是 install 或 uninstall，处理该命令
	if command != "" {
		switch command {
		case "install":
			installService(iface, ip, destroy)
			return
		case "uninstall":
			uninstallService()
			return
		}
	}

	// 检查必需的参数是否为空
	if iface == "" || ip == "" {
		fmt.Println("Usage: ospf -iface=<interface> -ip=<ip/cidr>")
		os.Exit(1)
	}

	// 解析IP地址
	p, err := netip.ParsePrefix(ip)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	ipNet := net.IPNet{
		IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), 32),
	}

	// 创建路由器
	router, err := ospf_cnn.NewRouter(iface, &ipNet, p.Addr().String())
	if err != nil {
		fmt.Println("Error creating router:", err)
		os.Exit(1)
	}

	// 启动路由器
	go router.Start()
	ospf_cnn.LogInfo("Router started")

	// 如果用户指定了 destroy 参数，监听关闭信号并在退出时关闭路由器
	if destroy {
		// 等待关闭信号
		stopApp(router)
	} else {
		// 使用 select{} 阻塞主线程
		select {}
	}
}

// 安装 OSPF 应用为 systemd 服务
func installService(iface, ip string, destroy bool) {
	// 获取当前程序的路径
	execPath, err := os.Executable()
	if err != nil {
		fmt.Println("Error getting executable path:", err)
		os.Exit(1)
	}

	// 填充 systemd 服务文件模板
	serviceFileContent := &struct {
		ExecPath    string
		IfaceFlag   string
		IpFlag      string
		DestroyFlag string
	}{
		ExecPath:    execPath,
		IfaceFlag:   fmt.Sprintf("-iface=%s", iface),
		IpFlag:      fmt.Sprintf("-ip=%s", ip),
		DestroyFlag: fmt.Sprintf("-destroy=%v", destroy),
	}

	// 生成 systemd 服务文件
	tmpl, err := template.New("service").Parse(serviceTemplate)
	if err != nil {
		fmt.Println("Error parsing service template:", err)
		os.Exit(1)
	}

	// 定义服务文件的路径
	serviceFilePath := "/etc/systemd/system/ospf-neighbor.service"
	file, err := os.Create(serviceFilePath)
	if err != nil {
		fmt.Println("Error creating service file:", err)
		os.Exit(1)
	}
	defer file.Close()

	// 将模板内容写入文件
	err = tmpl.Execute(file, serviceFileContent)
	if err != nil {
		fmt.Println("Error executing template:", err)
		os.Exit(1)
	}

	// 使服务文件生效
	cmd := exec.Command("systemctl", "daemon-reload")
	err = cmd.Run()
	if err != nil {
		fmt.Println("Error reloading systemd:", err)
		os.Exit(1)
	}

	// 启用并启动服务
	cmd = exec.Command("systemctl", "enable", "ospf-neighbor.service")
	err = cmd.Run()
	if err != nil {
		fmt.Println("Error enabling service:", err)
		os.Exit(1)
	}

	cmd = exec.Command("systemctl", "start", "ospf-neighbor.service")
	err = cmd.Run()
	if err != nil {
		fmt.Println("Error starting service:", err)
		os.Exit(1)
	}

	fmt.Println("OSPF Neighbor service installed and started successfully.")
}

// 卸载 OSPF 应用的 systemd 服务
func uninstallService() {
	// 定义服务文件路径
	serviceFilePath := "/etc/systemd/system/ospf-neighbor.service"

	// 停止并禁用服务
	cmd := exec.Command("systemctl", "stop", "ospf-neighbor.service")
	err := cmd.Run()
	if err != nil {
		fmt.Println("Error stopping service:", err)
		os.Exit(1)
	}

	cmd = exec.Command("systemctl", "disable", "ospf-neighbor.service")
	err = cmd.Run()
	if err != nil {
		fmt.Println("Error disabling service:", err)
		os.Exit(1)
	}

	// 删除服务文件
	err = os.Remove(serviceFilePath)
	if err != nil {
		fmt.Println("Error removing service file:", err)
		os.Exit(1)
	}

	// 重新加载 systemd 配置
	cmd = exec.Command("systemctl", "daemon-reload")
	err = cmd.Run()
	if err != nil {
		fmt.Println("Error reloading systemd:", err)
		os.Exit(1)
	}

	// 输出成功信息
	fmt.Println("OSPF Neighbor service uninstalled successfully.")
}

// 停止应用并优雅地关闭路由器
func stopApp(router *ospf_cnn.Router) {
	// 捕获系统终止信号（SIGINT 或 SIGTERM）
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 等待信号
	<-sigChan

	// 如果 destroy 参数为 true，关闭路由器
	ospf_cnn.LogInfo("Shutting down router...")
	err := router.Close()
	if err != nil {
		// 退出程序
		ospf_cnn.LogInfo("Router close failed.", err.Error())
		os.Exit(0)
		return
	}
	ospf_cnn.LogInfo("Router shut down successfully.")

	// 退出程序
	os.Exit(0)
}
