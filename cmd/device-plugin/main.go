// 项目三 D6:最小 GPU device plugin(拓扑上报骨架)。
//
// 作用:真机上把"有哪些卡 + 每卡在哪个 NUMA 域"上报给 kubelet,并支持
// Allocate 注入 CUDA_VISIBLE_DEVICES、GetPreferredAllocation 同域优先。
// 节点拓扑标签(LabelNUMADomain 等)可由上报结果派生 → 我们的 Score 插件消费。
//
// 用法(真机,非容器模拟环境):
//
//	sudo mkdir -p /var/lib/kubelet/device-plugins
//	sudo ./device-plugin --socket-dir /var/lib/kubelet/device-plugins
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	"github.com/fei/elastic-topo-scheduler/pkg/deviceplugin"
)

func main() {
	var (
		socketDir   = flag.String("socket-dir", "/var/lib/kubelet/device-plugins", "kubelet device plugin socket 目录")
		resourceName = flag.String("resource-name", "nvidia.com/gpu", "上报的资源名")
	)
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// 1. 真机扫描:GPU 列表 + 每卡 NUMA(sysfs)。
	gpus, err := deviceplugin.DiscoverGPUs("/")
	if err != nil {
		log.Fatalf("discover gpus: %v", err)
	}
	log.Printf("discovered %d GPU(s)", len(gpus))
	for _, g := range gpus {
		log.Printf("  nvidia-%d %s bus=%s numa=%d", g.Index, g.Model, g.BusID, g.NumaNode)
	}

	// 2. 起 gRPC server,监听 device-plugin 专属 socket。
	sock := filepath.Join(*socketDir, "topo-gpu.sock")
	if err := os.RemoveAll(sock); err != nil { // 清理陈旧 socket 文件
		log.Fatalf("clean socket: %v", err)
	}
	lis, err := net.Listen("unix", sock)
	if err != nil {
		log.Fatalf("listen %s: %v", sock, err)
	}
	server := grpc.NewServer()
	v1beta1.RegisterDevicePluginServer(server, deviceplugin.NewServer(gpus))
	go func() { _ = server.Serve(lis) }()
	log.Printf("serving on %s", sock)

	// 3. 向 kubelet 注册。
	if err := register(*socketDir, *resourceName, filepath.Base(sock)); err != nil {
		log.Fatalf("register with kubelet: %v", err)
	}
	log.Printf("registered %s, keep alive…", *resourceName)

	// 骨架:进程常驻(真正的 device plugin 会监听 health 检查 / 热插拔)。
	select {}
}

// register 把本 plugin 注册到 kubelet 的 device-plugins socket。
func register(socketDir, resourceName, pluginSocket string) error {
	conn, err := grpc.NewClient(
		"unix://"+filepath.Join(socketDir, "kubelet.sock"),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = v1beta1.NewRegistrationClient(conn).Register(ctx, &v1beta1.RegisterRequest{
		Version:      v1beta1.Version,
		Endpoint:     pluginSocket,
		ResourceName: resourceName,
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	return nil
}
