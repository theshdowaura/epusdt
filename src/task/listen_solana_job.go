package task

import (
	"github.com/assimon/luuu/model/data"
	"github.com/assimon/luuu/model/service"
	"github.com/assimon/luuu/util/log"
	"sync"
)

type ListenSolanaJob struct{}

var gListenSolanaJobLock sync.Mutex

func (r ListenSolanaJob) Run() {
	gListenSolanaJobLock.Lock()
	defer gListenSolanaJobLock.Unlock()
	walletAddress, err := data.GetAvailableWalletAddress()
	if err != nil {
		log.Sugar.Error(err)
		return
	}
	if len(walletAddress) <= 0 {
		return
	}
	var wg sync.WaitGroup
	for _, address := range walletAddress {
		wg.Add(1)
		go service.SolanaCallBack(address.Token, &wg)
	}
	wg.Wait()
}
