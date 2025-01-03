### 一个简单的ospf邻居添加器
应用运行中会在指定接口添加ospf邻居

api:

`http://{server-ip}:{port}/restart`： 重启

使用示例


``` shell
./ospf-neighbor -iface=eth0 -ip=192.168.1.24/24
```

``` text
Usage of ./ospf-neighbor:
  -destroy
        If true, destroy the router on exit
  -iface string
        Network interface name
  -ip string
        Local IP address with CIDR (e.g., 192.168.1.2/24)
  -port string
        http server port. default 8796
```

### 安装为服务
``` shell
./ospf-neighbor install -iface=eth0 -ip=192.168.1.24/24
```

### 卸载服务
``` shell
./ospf-neighbor uninstall
```
### 致谢
ospf调用相关代码来源@povsister
https://github.com/povsister/v2ray-core
