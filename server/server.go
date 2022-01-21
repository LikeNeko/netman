package server

import (
	"fmt"
	"log"
	"runtime"

	"github.com/ikilobyte/netman/eventloop"

	"github.com/ikilobyte/netman/util"

	"github.com/ikilobyte/netman/iface"
)

type serverStatus = int

const (
	stopped  serverStatus = iota // 已停止（初始状态）
	started                      // 已启动
	stopping                     // 停止中
)

type Server struct {
	ip         string
	port       int
	status     serverStatus          // 状态
	options    *Options              // serve启动可选项参数
	socket     *socket               // 直接系统调用的方式监听TCP端口，不使用官方的net包
	eventloop  iface.IEventLoop      // 事件循环管理
	connectMgr iface.IConnectManager // 所有的连接管理
	packer     iface.IPacker         // 负责封包解包
	emitCh     chan iface.IRequest   // 从这里接收epoll转发过来的消息，然后交给worker去处理
	routerMgr  *RouterMgr            // 路由统一管理
}

//New 创建Server
func New(ip string, port int, opts ...Option) *Server {

	options := parseOption(opts...)

	// 使用几个事件循环管理连接
	if options.NumEventLoop <= 0 {
		options.NumEventLoop = runtime.NumCPU()
	}

	// 封包解包的实现层，外部可以自行实现IPacker使用自己的封包解包方式
	if options.Packer == nil {
		options.Packer = util.NewDataPacker()
	}

	// 日志保存路径
	if options.LogOutput != nil {
		util.Logger.SetOutput(options.LogOutput)
	}

	// 初始化
	server := &Server{
		ip:         ip,
		port:       port,
		options:    options,
		status:     stopped,
		socket:     createSocket(fmt.Sprintf("%s:%d", ip, port), options.TCPKeepAlive),
		eventloop:  eventloop.NewEventLoop(options.NumEventLoop),
		connectMgr: NewConnectManager(),
		emitCh:     make(chan iface.IRequest, 128),
		packer:     options.Packer,
		routerMgr:  NewRouterMgr(),
	}

	// 初始化epoll
	if err := server.eventloop.Init(server.connectMgr); err != nil {
		log.Panicln(err)
	}

	// 执行wait
	server.eventloop.Start(server.emitCh)

	// 接收消息的处理，
	go func() {
		for {
			select {
			case request, ok := <-server.emitCh:

				// 通道已关闭
				if !ok {
					return
				}

				// 交给路由管理中心去处理，执行业务逻辑
				if err := server.routerMgr.Do(request); err != nil {
					util.Logger.Infoln(fmt.Errorf("do handler err %s", err))
				}
			}
		}
	}()

	return server
}

//AddRouter 添加路由处理
func (s *Server) AddRouter(msgID uint32, router iface.IRouter) {
	s.routerMgr.Add(msgID, router)
}

//Start 启动
func (s *Server) Start() {
	if s.status != stopped {
		return
	}
	s.status = started
	util.Logger.WithField("ip", s.ip).WithField("port", s.port).Info("server started")
	for {
		conn, err := s.socket.Accept(s.packer)
		if err != nil {
			util.Logger.Errorf("socket Accept error %v", err)
			continue
		}

		// 添加到epoll中
		if err := s.eventloop.AddRead(conn); err != nil {
			// 断开连接
			_ = conn.Close()
			continue
		}

		// 添加到统一管理
		total := s.connectMgr.Add(conn)

		util.Logger.
			WithField("conn_id", conn.GetID()).
			WithField("address", conn.GetAddress().String()).
			WithField("conn_total", total).
			Info("new connect")
	}
}

//Stop 停止
func (s *Server) Stop() {

	// 1、设置状态
	s.status = stopping

	// 2、删除所有停止所有epoll
	s.eventloop.Stop()

	// 3、断开所有连接
	s.connectMgr.ClearAll()

	// 4、停止服务
	close(s.emitCh)

	// 5、设置状态
	s.status = stopped

}
