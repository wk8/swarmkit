VAGRANTFILE_API_VERSION = '2'
VM_NAME = 'swarmkit-dev'

HOME = '/home/vagrant'

REPO_ROOT_DIR = File.expand_path('..', __FILE__)

SYNC_EXCLUDES = [
  '_obj',
  '_test',
  'bin',
  '.vagrant',
  '*.log'
]

Vagrant.configure(VAGRANTFILE_API_VERSION) do |config|
  config.vm.hostname = VM_NAME

  config.vm.box = 'ubuntu/xenial64'

  config.vm.provider 'virtualbox' do |vb|
    vb.name = VM_NAME

    # we want more CPUs, and more RAM!
    vb.customize ['modifyvm', :id, '--ioapic', 'on']
    vb.customize ['modifyvm', :id, '--cpus', '4']
    vb.customize ['modifyvm', :id, '--memory', '8192']

    # allow the guest machine to access the internet through the host
    vb.customize ['modifyvm', :id, '--natdnshostresolver1', 'on']
    vb.customize ['modifyvm', :id, '--natdnsproxy1', 'on']
  end

  go_path = File.join(HOME, 'go')
  go_version = '1.10'
  repo_path = File.join(go_path, 'src/github.com/docker/swarmkit')

  config.vm.synced_folder REPO_ROOT_DIR, repo_path, \
    type: 'rsync', \
    rsync__exclude: SYNC_EXCLUDES

  path = [
    '/usr/local/sbin',
    '/usr/local/bin',
    '/usr/sbin',
    '/usr/bin',
    '/sbin',
    '/bin',
    "/usr/lib/go-#{go_version}/bin",
    File.join(go_path, 'bin')
  ].join(':')

  environment = {
    'PATH' => path,
    'GOPATH' => go_path
  }.map{ |k, v| "#{k}=#{v}" }
 
  script = <<-SCRIPT
set -ex
sudo chown -R vagrant:vagrant #{go_path}
ln -sf #{repo_path} #{File.join(HOME, 'swarmkit')}
sudo apt-get update
sudo apt-get install -qy build-essential golang-#{go_version}
echo -e '#{environment.join("\n")}' > /etc/environment
cd #{repo_path} && #{environment.join(' ')} make setup
SCRIPT

  config.vm.provision 'shell', inline: script

  if Vagrant.has_plugin?('vagrant-gatling-rsync')
    # duh. Don't get in the way of my VM starting, you dumb gatling
    config.gatling.rsync_on_startup = false
  end
end
