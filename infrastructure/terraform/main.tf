# Generate SSH key if not provided
resource "tls_private_key" "ssh" {
  count     = var.ssh_public_key == "" ? 1 : 0
  algorithm = "ED25519"
}

resource "local_file" "ssh_private_key" {
  count           = var.ssh_public_key == "" ? 1 : 0
  content         = tls_private_key.ssh[0].private_key_openssh
  filename        = "${path.module}/../../.ssh/id_ed25519"
  file_permission = "0600"
}

resource "local_file" "ssh_public_key" {
  count           = var.ssh_public_key == "" ? 1 : 0
  content         = tls_private_key.ssh[0].public_key_openssh
  filename        = "${path.module}/../../.ssh/id_ed25519.pub"
  file_permission = "0644"
}

locals {
  ssh_public_key = var.ssh_public_key != "" ? var.ssh_public_key : tls_private_key.ssh[0].public_key_openssh
  k3s_token      = var.k3s_token != "" ? var.k3s_token : random_password.k3s_token[0].result
}

resource "random_password" "k3s_token" {
  count   = var.k3s_token == "" ? 1 : 0
  length  = 64
  special = false
}

# SSH Key for VM access
resource "hcloud_ssh_key" "cluster" {
  name       = "${var.cluster_name}-cluster-key"
  public_key = local.ssh_public_key
}

# Private network for cluster communication
resource "hcloud_network" "cluster" {
  name     = "${var.cluster_name}-network"
  ip_range = "10.0.0.0/8"
}

# Subnet for eu-central (covers fsn1, nbg1)
resource "hcloud_network_subnet" "eu_central" {
  network_id   = hcloud_network.cluster.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.1.0.0/16"
}

# Subnet for Helsinki
resource "hcloud_network_subnet" "helsinki" {
  network_id   = hcloud_network.cluster.id
  type         = "cloud"
  network_zone = "eu-central" # hel1 is also eu-central zone
  ip_range     = "10.2.0.0/16"
}

# Master node in Falkenstein
resource "hcloud_server" "master" {
  name        = "${var.cluster_name}-master"
  server_type = var.master_server_type
  image       = "ubuntu-24.04"
  location    = "fsn1"
  ssh_keys    = [hcloud_ssh_key.cluster.id]

  labels = {
    role    = "master"
    region  = "fsn1"
    cluster = var.cluster_name
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }

  network {
    network_id = hcloud_network.cluster.id
    ip         = "10.1.0.10"
  }

  depends_on = [hcloud_network_subnet.eu_central]
}

# Worker nodes across regions
resource "hcloud_server" "workers" {
  for_each = toset(var.regions)

  name        = "${var.cluster_name}-worker-${each.key}"
  server_type = var.worker_server_type
  image       = "ubuntu-24.04"
  location    = each.key
  ssh_keys    = [hcloud_ssh_key.cluster.id]

  labels = {
    role    = "worker"
    region  = each.key
    cluster = var.cluster_name
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }

  network {
    network_id = hcloud_network.cluster.id
    ip         = each.key == "fsn1" ? "10.1.0.11" : (each.key == "nbg1" ? "10.1.0.12" : "10.2.0.11")
  }

  depends_on = [hcloud_network_subnet.eu_central, hcloud_network_subnet.helsinki]
}

# Volumes for persistent storage (attached to fsn1 worker)
resource "hcloud_volume" "postgres" {
  name     = "${var.cluster_name}-postgres"
  size     = 50
  location = "fsn1"
  format   = "ext4"
}

resource "hcloud_volume" "redis" {
  name     = "${var.cluster_name}-redis"
  size     = 10
  location = "fsn1"
  format   = "ext4"
}

resource "hcloud_volume" "minio" {
  name     = "${var.cluster_name}-minio"
  size     = 50
  location = "fsn1"
  format   = "ext4"
}

resource "hcloud_volume_attachment" "postgres" {
  volume_id = hcloud_volume.postgres.id
  server_id = hcloud_server.workers["fsn1"].id
  automount = true
}

resource "hcloud_volume_attachment" "redis" {
  volume_id = hcloud_volume.redis.id
  server_id = hcloud_server.workers["fsn1"].id
  automount = true
}

resource "hcloud_volume_attachment" "minio" {
  volume_id = hcloud_volume.minio.id
  server_id = hcloud_server.workers["fsn1"].id
  automount = true
}

# Firewall
resource "hcloud_firewall" "cluster" {
  name = "${var.cluster_name}-firewall"

  # SSH
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # Kubernetes API
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "6443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # HTTP
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # HTTPS
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # Flannel VXLAN (internal)
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "8472"
    source_ips = ["10.0.0.0/8"]
  }

  # Kubelet (internal)
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "10250"
    source_ips = ["10.0.0.0/8"]
  }

  # NodePort range
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "30000-32767"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # WireGuard for Flannel
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "51820"
    source_ips = ["10.0.0.0/8"]
  }
}

resource "hcloud_firewall_attachment" "cluster" {
  firewall_id = hcloud_firewall.cluster.id
  server_ids = concat(
    [hcloud_server.master.id],
    [for w in hcloud_server.workers : w.id]
  )
}

# Generate kubeconfig setup script
resource "local_file" "setup_env" {
  content = templatefile("${path.module}/templates/setup-env.sh.tpl", {
    master_ip       = hcloud_server.master.ipv4_address
    master_private  = "10.1.0.10"
    k3s_token       = local.k3s_token
    workers         = { for k, v in hcloud_server.workers : k => v.ipv4_address }
    ssh_key_path    = var.ssh_public_key == "" ? "${path.module}/../../.ssh/id_ed25519" : ""
  })
  filename        = "${path.module}/../../setup-env.sh"
  file_permission = "0755"
}
