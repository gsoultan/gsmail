package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/smtp"
)

func main() {
	// 1. Create a basic sender for your SMTP server.
	// NewSender(host, port, user, pass, isSSL)
	sender := smtp.NewSender("smtp.example.com", 587, "no-reply@example.com", "example", true)

	// 2. Configure the Advanced Connection Pool.
	// This allows high-concurrency sending with efficient resource reuse.
	config := smtp.PoolConfig{
		MaxIdle:     10,              // Keep up to 10 connections idle for reuse.
		MaxOpen:     1,               // Limit the total number of open connections to 50.
		IdleTimeout: 5 * time.Minute, // Close connections that have been idle for too long.
		MaxLifetime: 1 * time.Hour,   // Periodically refresh connections to prevent stale connections.
		Wait:        true,            // If the pool is full, wait for a connection to become available.
	}
	sender.EnablePool(config)

	// 3. Define the email you want to send.
	// Note the "From Name <email@example.com>" format.
	email := gsmail.Email{
		From:    "Test <no-reply@example.com>",
		To:      []string{"Dimas Prananda <gembit@example.com>"},
		Subject: "Your Order is Ready!",
		Body:    []byte("<h1>Hello!</h1>Your order Dimas has been processed and is ready for shipment."),
	}

	// 4. Send the email.
	// The Send() method automatically handles retries if configured and manages the connection pool.
	if err := sender.Send(context.Background(), email); err != nil {
		log.Fatalf("Failed to send email: %v", err)
	}
	fmt.Println("Email sent successfully via SMTP!")

	// 5. Example of high-concurrency sending (15 emails).
	fmt.Println("Starting high-concurrency send (15 emails)...")
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			e := email
			e.Subject = fmt.Sprintf("Message #%d", id)
			if err := sender.Send(context.Background(), e); err != nil {
				log.Printf("Failed to send message #%d: %v", id, err)
			} else {
				fmt.Printf("Message #%d sent successfully\n", id)
			}
		}(i)
	}

	// 6. Wait for all emails to be sent.
	wg.Wait()

	// 7. Monitor performance with Pool Stats.
	stats, _ := sender.PoolStats()
	fmt.Printf("Pool Stats - Open: %d, Idle: %d, Wait Count: %d, Wait Duration: %v\n",
		stats.OpenConnections, stats.IdleConnections, stats.WaitCount, stats.WaitDuration)

	// 8. Graceful Shutdown.
	// Close() will gracefully shut down all connections in the pool.
	if err := sender.Close(); err != nil {
		log.Printf("Error closing sender: %v", err)
	}
	fmt.Println("Sender closed. Example finished.")
}
