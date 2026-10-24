# Kafka consumer lag

Owner: streaming-platform.

## Symptoms
Consumer group lag growing on `orders.events`; downstream emails and analytics delayed.

## Mitigation
1. Check for a poison message: the same offset failing repeatedly in consumer logs. Move it to the dead-letter topic.
2. Scale consumers up to the partition count (12). More consumers than partitions do nothing.
3. If a rebalance storm is visible, raise `max.poll.interval.ms`.
