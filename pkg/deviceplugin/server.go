package deviceplugin

// DevicePluginServer 的薄壳实现(k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1)。
//
// 面试要点(为什么这么写):
//   ListAndWatch —— 把发现的卡以 Device{ID, Topology.NUMANodeID} 流式上报给 kubelet。
//        Topology 字段就是节点拓扑标签(LabelNUMADomain)的数据来源:device plugin
//        把"每卡在哪个 NUMA 域"告诉 kubelet → 节点标签 → 我们的 Score 插件消费。
//   GetPreferredAllocation —— kubelet 在做设备分配前问插件"这批候选卡里你偏好哪些"。
//        我们实现 = 与 MustInclude 同 NUMA 域优先,这是调度器邻近度思想在设备层的对应物。
//   Allocate —— 分配时把选中的卡注入 CUDA_VISIBLE_DEVICES(容器里看到的就是这几张)。

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// gpuServer 实现 v1beta1.DevicePluginServer。devices 是发现结果快照(启动时扫描)。
// 内嵌 UnimplementedDevicePluginServer 兜底未来新增方法(接口向前兼容)。
type gpuServer struct {
	devices []*v1beta1.Device
	byID    map[string]int // device ID → numa node;未知 = -1
	v1beta1.UnimplementedDevicePluginServer
}

// NewServer 把 GPUInfo 列表包装成 gRPC server(按 Index 升序 → Device ID "nvidia-N")。
func NewServer(gpus []GPUInfo) v1beta1.DevicePluginServer {
	s := &gpuServer{byID: map[string]int{}}
	for _, g := range gpus {
		id := fmt.Sprintf("nvidia-%d", g.Index)
		dev := &v1beta1.Device{ID: id, Health: v1beta1.Healthy}
		if g.NumaNode >= 0 {
			// v1beta1 的 TopologyInfo 字段是 Nodes []*NUMANode(v1.30+ 从 NUMANodeID 重构而来)
			dev.Topology = &v1beta1.TopologyInfo{Nodes: []*v1beta1.NUMANode{{ID: int64(g.NumaNode)}}}
		}
		s.devices = append(s.devices, dev)
		s.byID[id] = g.NumaNode
	}
	return s
}

// GetDevicePluginOptions 声明我们支持 PreferredAllocation。
func (s *gpuServer) GetDevicePluginOptions(context.Context, *v1beta1.Empty) (*v1beta1.DevicePluginOptions, error) {
	return &v1beta1.DevicePluginOptions{GetPreferredAllocationAvailable: true}, nil
}

// ListAndWatch 流式上报设备列表。先发一次全量,然后挂起等变更通道(骨架:无动态热插拔)。
func (s *gpuServer) ListAndWatch(_ *v1beta1.Empty, stream v1beta1.DevicePlugin_ListAndWatchServer) error {
	if err := stream.Send(&v1beta1.ListAndWatchResponse{Devices: s.devices}); err != nil {
		return err
	}
	// 骨架:阻塞等待,直到 stream 关闭。真机热插拔时在此监听 /dev/nvidia* 变更再重发。
	// 监听 context 关闭(kubelet 断开)→ 返回,避免 goroutine 泄漏(代码审查 P1 修复)。
	<-stream.Context().Done()
	return stream.Context().Err()
}

// GetPreferredAllocation 同 NUMA 域优先:先取 MustInclude,再从 Available 里
// 挑与已选同域、且不超过 AllocatableDevices 的卡补足。这是"邻近度优先"的设备层实现。
func (s *gpuServer) GetPreferredAllocation(_ context.Context, req *v1beta1.PreferredAllocationRequest) (*v1beta1.PreferredAllocationResponse, error) {
	resp := &v1beta1.PreferredAllocationResponse{}
	for _, cr := range req.ContainerRequests {
		chosen := append([]string{}, cr.MustIncludeDeviceIDs...)
		domain := s.domainOf(chosen) // 已选卡的 NUMA 域;-1 = 混合/未知
		for _, id := range cr.AvailableDeviceIDs {
			if int32(len(chosen)) >= cr.AllocationSize {
				break
			}
			if contains(chosen, id) {
				continue
			}
			// 优先补同域;域为 -1 时全部可用(无法判断就不武断)
			if domain == -1 || s.byID[id] == domain {
				chosen = append(chosen, id)
			}
		}
		resp.ContainerResponses = append(resp.ContainerResponses,
			&v1beta1.ContainerPreferredAllocationResponse{DeviceIDs: chosen})
	}
	return resp, nil
}

// Allocate 把选中卡映射成容器环境变量 CUDA_VISIBLE_DEVICES(骨架只做 env,不含驱动挂载)。
func (s *gpuServer) Allocate(_ context.Context, req *v1beta1.AllocateRequest) (*v1beta1.AllocateResponse, error) {
	resp := &v1beta1.AllocateResponse{}
	for _, cr := range req.ContainerRequests {
		indices := make([]string, 0, len(cr.DevicesIds))
		for _, id := range cr.DevicesIds {
			// "nvidia-0" → "0";未知 ID 则原样透传(骨架容错,与 Phase 0 同构)
			idx, ok := strings.CutPrefix(id, "nvidia-")
			if ok {
				indices = append(indices, idx)
			} else {
				indices = append(indices, id)
			}
		}
		resp.ContainerResponses = append(resp.ContainerResponses, &v1beta1.ContainerAllocateResponse{
			Envs: map[string]string{"CUDA_VISIBLE_DEVICES": strings.Join(indices, ",")},
		})
	}
	return resp, nil
}

// PreStartContainer 骨架不需要(不注入额外挂载,GetDevicePluginOptions 已声明不需要)。
func (s *gpuServer) PreStartContainer(context.Context, *v1beta1.PreStartContainerRequest) (*v1beta1.PreStartContainerResponse, error) {
	return &v1beta1.PreStartContainerResponse{}, nil
}

// domainOf 返回一组卡共同的 NUMA 域;不一致或未知返回 -1(不武断)。
func (s *gpuServer) domainOf(ids []string) int {
	d := -1
	for _, id := range ids {
		n, ok := s.byID[id]
		if !ok {
			return -1
		}
		if d == -1 {
			d = n
		} else if d != n {
			return -1
		}
	}
	return d
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
