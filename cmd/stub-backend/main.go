package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

func main() {
	var newConns atomic.Int64 // nº de ligações TCP novas que o stub já viu

	mux := http.NewServeMux()
	
	// Resposta imediata. 
	mux.HandleFunc("/fast", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Dorme N ms. Se o pedido for cancelado a meio, regista-o.
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		select {
		case <- time.After(time.Duration(ms) * time.Millisecond):
			w.Write([]byte("done"))
		case <- r.Context().Done():
			// Prova de que o cancelamento chegou ao backend.
			log.Printf("slow: pedido cancelado (%v)", r.Context().Err())

		}
	})

	mux.HandleFunc("/error", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	// Mostra os cabeçalhos que o backend recebeu (útil no Passo 10).
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		for k, v := range r.Header {
			fmt.Fprintf(w, "%s: %v\n", k, v)
		}
	})

	// Quantas ligações TCP novas já chegaram aqui.
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d\n", newConns.Load())
	})

	srv := &http.Server{
		Addr:    ":9000",
		Handler: mux,
		ConnState: func(c net.Conn, s http.ConnState) {
			if s == http.StateNew {
				newConns.Add(1)
			}
		},
	}
	log.Println("stub-backend em :9000")
	log.Fatal(srv.ListenAndServe())
}