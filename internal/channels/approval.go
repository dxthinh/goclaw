package channels

// TaskApprovalCapable là năng lực duyệt task qua lệnh chat (/approve,
// /changes) của một kênh. Chỉ kênh đã cài lệnh duyệt mới cài interface này;
// không dùng danh sách loại kênh cố định.
type TaskApprovalCapable interface {
	// TaskApprovalEnabled cho biết instance kênh đã bật task_approval.
	TaskApprovalEnabled() bool
}

// ChannelSupportsTaskApproval tra kênh theo tên và cho biết kênh có cài lệnh
// duyệt và đã bật task_approval hay không.
func ChannelSupportsTaskApproval(mgr *Manager, channelName string) bool {
	if mgr == nil || channelName == "" {
		return false
	}
	ch, ok := mgr.GetChannel(channelName)
	if !ok {
		return false
	}
	capable, ok := ch.(TaskApprovalCapable)
	return ok && capable.TaskApprovalEnabled()
}
