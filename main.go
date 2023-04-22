package main

import (
	"fmt"
	"github.com/kaspanet/kaspad/app/appmessage"
	"github.com/kaspanet/kaspad/infrastructure/network/rpcclient"
	"github.com/kaspanet/kaspad/util/profiling"
	"os"
	"sync/atomic"
	"time"

	"github.com/kaspanet/kaspad/infrastructure/os/signal"
	"github.com/kaspanet/kaspad/util/panics"
)

var shutdown int32 = 0

func main() {
	interrupt := signal.InterruptListener()
	err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing config: %+v", err)
		os.Exit(1)
	}
	defer backendLog.Close()

	defer panics.HandlePanic(log, "main", nil)

	if cfg.Profile != "" {
		profiling.Start(cfg.Profile, log)
	}

	addresses, err := loadAddresses()
	if err != nil {
		panic(err)
	}

	rpcAddress, err := activeConfig().ActiveNetParams.NormalizeRPCServerAddress(activeConfig().RPCServer)
	if err != nil {
		panic(err)
	}

	client, err := rpcclient.NewRPCClient(rpcAddress)
	if err != nil {
		panic(err)
	}

	client.SetTimeout(5 * time.Minute)

	utxosChangedNotificationChan := make(chan *appmessage.UTXOsChangedNotificationMessage, 100)
	err = client.RegisterForUTXOsChangedNotifications([]string{addresses.myAddress.EncodeAddress()},
		func(notification *appmessage.UTXOsChangedNotificationMessage) {
			utxosChangedNotificationChan <- notification
		})
	if err != nil {
		panic(err)
	}

	highest_tx_count := 0
	err = client.RegisterForBlockAddedNotifications(
		func(notification *appmessage.BlockAddedNotificationMessage) {
			num_of_txs := len(notification.Block.VerboseData.TransactionIDs)
			log.Infof("Found block %s with %d Txs", notification.Block.VerboseData.Hash, num_of_txs)
			if num_of_txs > highest_tx_count {
				highest_tx_count = num_of_txs
				log.Infof("New Highest Tx per block: %d", highest_tx_count)
			}
		})
	if err != nil {
		panic(err)
	}

	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for {
		select {
			case <- ticker.C:
				orphanCount := 0
				getMempoolEntries, err := client.GetMempoolEntries(true, false)
				if err != nil {
					panic(err)
				}
				mempoolSize := len(getMempoolEntries.Entries)
				for _, mempoolEntry := range getMempoolEntries.Entries {
					if mempoolEntry.IsOrphan {
						orphanCount += 1
					}
				}
				log.Infof("Mempool Occupied: %d , Orphans: %d", mempoolSize, orphanCount)
			}
		}
	}()

	spendLoopDoneChan := spendLoop(client, addresses, utxosChangedNotificationChan)

	<-interrupt

	atomic.AddInt32(&shutdown, 1)

	<-spendLoopDoneChan
}
