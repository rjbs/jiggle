package Jiggle::TestGo;
use v5.36;

use Exporter 'import';
our @EXPORT = qw( go_or_skip jiggle_go jiggle_go_serving library_to_migrate scratch_file );

use IO::Socket::INET ();
use Jiggle::TestLibrary;
use Path::Tiny ();
use Test::More ();

=head1 NAME

Jiggle::TestGo - run jiggle-go, the Go half of jiggle, in tests

=head1 DESCRIPTION

Until the Go server takes over, the two halves are tested against each
other: a library is made as Perl jiggle keeps it, migrated to a database,
and what Go makes of that is compared with what Perl makes of the library.
These tests need Go, and are skipped without it.

=cut

my $TMP = Path::Tiny->tempdir;
my $GO;
my $N = 0;

=func go_or_skip

This builds F<jiggle-go>, or skips the whole test file if Go isn't
installed.  Call it before anything else.

=cut

sub go_or_skip {
  Test::More::plan(skip_all => 'go is not installed')
    unless grep {; -x "$_/go" } split /:/, $ENV{PATH};

  $GO = $TMP->child('jiggle-go');
  system('go', 'build', '-o', "$GO", './cmd/jiggle-go') == 0
    or Test::More::BAIL_OUT("can't build jiggle-go");
  return;
}

=func scratch_file

  my $file = scratch_file('db', 'sqlite');

This returns a path, in a directory that lasts as long as the test, at which
there is no file yet.

=cut

sub scratch_file ($name, $ext) { $TMP->child($name . '-' . $N++ . ".$ext") }

=func jiggle_go

  my ($status, $output) = jiggle_go(migrate => '--library', $root, '--out', $db);

This runs F<jiggle-go>, and returns its exit status and everything it
printed.

=cut

sub jiggle_go (@args) {
  my $out = scratch_file('out', 'txt');

  my $pid = fork // die "can't fork: $!";
  unless ($pid) {
    open STDOUT, '>', "$out" or die "can't write $out: $!";
    open STDERR, '>&', \*STDOUT or die "can't dup: $!";
    exec "$GO", map {; "$_" } @args or die "can't run $GO: $!";
  }
  waitpid $pid, 0;

  return ($? >> 8, $out->slurp_utf8);
}

=func jiggle_go_serving

  my $url = jiggle_go_serving($db, $root);

This starts C<jiggle-go serve> on the database and library given, on a port
of this machine that nothing else is using, and returns its URL once it's
answering.  The server is stopped when the test ends.

=cut

my @SERVERS;
END { kill TERM => @SERVERS if @SERVERS }

sub jiggle_go_serving ($db, $root) {
  my $port = do {
    my $socket = IO::Socket::INET->new(Listen => 1, LocalAddr => '127.0.0.1', LocalPort => 0)
      or die "can't find a port: $!";
    $socket->sockport;
  };

  my $log = scratch_file('serve', 'log');
  my $pid = fork // die "can't fork: $!";
  unless ($pid) {
    open STDOUT, '>', "$log" or die "can't write $log: $!";
    open STDERR, '>&', \*STDOUT or die "can't dup: $!";
    exec "$GO", 'serve', '--db', "$db", '--library', "$root", '--listen', "127.0.0.1:$port"
      or die "can't run $GO: $!";
  }
  push @SERVERS, $pid;

  for (1 .. 100) {
    return "http://127.0.0.1:$port" if IO::Socket::INET->new(PeerAddr => '127.0.0.1', PeerPort => $port);
    select undef, undef, undef, 0.1;
  }
  Test::More::BAIL_OUT("jiggle-go serve never answered: " . $log->slurp_utf8);
}

=func library_to_migrate

  my $root = library_to_migrate(%library_args);

This makes a library with L<Jiggle::TestLibrary/library_with>, and gives
each photo a one-byte original, since migrate insists on originals, and a
digest of its own, since no two photos can have one original.  It returns
the library's root.

=cut

sub library_to_migrate (%arg) {
  $arg{photos} = [
    map {;
      +{
        original => {
          file => "$_->{id}.jpg", ext => 'jpg', bytes => 1, width => 4000, height => 3000,
          sha256 => $_->{id} . '0' x (64 - length $_->{id}),
        },
        %$_,
      }
    } $arg{photos}->@*
  ];

  my ($library, $root) = library_with(%arg);
  for my $photo ($library->photos) {
    my $file = $library->original_path($photo);
    $file->parent->mkpath;
    $file->spew_raw('x');
  }
  return $root;
}

1;
