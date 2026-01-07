output "master_ip" {
  description = "Public IP of the master node"
  value       = hcloud_server.master.ipv4_address
}

output "master_private_ip" {
  description = "Private IP of the master node"
  value       = "10.1.0.10"
}

output "worker_ips" {
  description = "Public IPs of worker nodes"
  value       = { for k, v in hcloud_server.workers : k => v.ipv4_address }
}

output "worker_private_ips" {
  description = "Private IPs of worker nodes"
  value = {
    fsn1 = "10.1.0.11"
    nbg1 = "10.1.0.12"
    hel1 = "10.2.0.11"
  }
}

output "k3s_token" {
  description = "Token for joining k3s cluster"
  value       = local.k3s_token
  sensitive   = true
}

output "ssh_private_key_path" {
  description = "Path to SSH private key (if generated)"
  value       = var.ssh_public_key == "" ? "${path.module}/../../.ssh/id_ed25519" : "Using provided key"
}

output "cluster_name" {
  description = "Cluster name"
  value       = var.cluster_name
}

output "ssh_command_master" {
  description = "SSH command to connect to master"
  value       = "ssh -i ${var.ssh_public_key == "" ? "${path.module}/../../.ssh/id_ed25519" : "YOUR_KEY"} root@${hcloud_server.master.ipv4_address}"
}
