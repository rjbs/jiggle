package Jiggle::Progress;
use v5.36;

use Moo;

use Time::HiRes ();

=head1 NAME

Jiggle::Progress - occasional progress reports for long jobs

=head1 SYNOPSIS

  my $progress = Jiggle::Progress->new({
    label  => 'renditions',
    total  => scalar @photos,
    logger => $logger,
  });

  for my $photo (@photos) {
    ...;
    $progress->tick;
  }

  $progress->done;

=head1 DESCRIPTION

This reports how far along a job is, at most once per C<interval> seconds
(default: 30), like:

  renditions: 4210/12480 (34%), 5.2/s, about 26m left

or, if C<total> isn't given, just the count and rate so far.  C<done> reports
the total:

  renditions: 12480 in 40m12s (5.2/s)

=cut

has label    => (is => 'ro', required => 1);
has total    => (is => 'ro');   # undef when not known in advance
has logger   => (is => 'ro', required => 1);
has interval => (is => 'ro', default  => 30);

has _count => (is => 'rw', init_arg => undef, default => 0);
has _start => (is => 'ro', init_arg => undef, default => sub { Time::HiRes::time() });
has _last  => (is => 'rw', init_arg => undef, default => sub { Time::HiRes::time() });

sub tick ($self, $n = 1) {
  $self->_count($self->_count + $n);

  my $now = Time::HiRes::time();
  return if $now - $self->_last < $self->interval;
  $self->_last($now);

  my $count   = $self->_count;
  my $elapsed = $now - $self->_start;
  my $rate    = $elapsed ? $count / $elapsed : 0;
  my $left    = $rate && defined $self->total ? ($self->total - $count) / $rate : 0;

  unless (defined $self->total) {
    $self->logger->(sprintf '%s: %d so far, %.1f/s', $self->label, $count, $rate);
    return;
  }

  $self->logger->(sprintf '%s: %d/%d (%d%%), %.1f/s, about %s left',
    $self->label, $count, $self->total,
    $self->total ? 100 * $count / $self->total : 100,
    $rate, _duration($left),
  );
}

sub done ($self) {
  my $elapsed = Time::HiRes::time() - $self->_start;
  $self->logger->(sprintf '%s: %d in %s (%.1f/s)',
    $self->label, $self->_count, _duration($elapsed),
    $elapsed ? $self->_count / $elapsed : 0,
  );
}

sub _duration ($seconds) {
  $seconds = int($seconds + 0.5);
  return sprintf '%dh%02dm', int($seconds / 3600), int($seconds % 3600 / 60)
    if $seconds >= 3600;
  return sprintf '%dm%02ds', int($seconds / 60), $seconds % 60 if $seconds >= 60;
  return "${seconds}s";
}

1;
