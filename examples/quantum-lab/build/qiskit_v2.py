from qiskit import QuantumCircuit
from qiskit_aer import AerSimulator
from qiskit_aer.noise import NoiseModel, depolarizing_error, ReadoutError
from qiskit.quantum_info import hellinger_fidelity
import json
import numpy as np

# Define the noise model
nm = NoiseModel()
nm.add_all_qubit_quantum_error(depolarizing_error(0.02, 1), ['u1', 'u2', 'u3'])
nm.add_all_qubit_quantum_error(depolarizing_error(0.04, 2), ['cx'])
nm.add_all_qubit_readout_error(ReadoutError([[0.95, 0.05], [0.03, 0.97]]))

# Create the simulator with the noise model
sim = AerSimulator(noise_model=nm)

# Ideal counts and noisy counts
ideal_counts = {'00': 2009, '11': 2087}
noisy_counts = {'00': 1824, '01': 198, '10': 193, '11': 1881}

# Total counts
total_counts = 4096

# Calculate ideal probabilities
ideal_probs = {bs: count / total_counts for bs, count in ideal_counts.items()}
noisy_probs = {bs: count / total_counts for bs, count in noisy_counts.items()}

# Calculate fidelity and error rate
fidelity = hellinger_fidelity(ideal_probs, noisy_probs)
error_rate = 1 - fidelity  # 1 - Fidelity

# Calibration circuits for assignment matrix
basis = ['00', '01', '10', '11']
cal = {}
for bs in basis:
    c = QuantumCircuit(2, 2)
    for i, b in enumerate(bs):
        if b == '1': c.x(i)
    c.measure([0, 1], [0, 1])
    cal[bs] = sim.run(c, shots=4096).result().get_counts()

# Construct assignment matrix
A = np.zeros((4, 4))  # assignment matrix A[observed][true]
for j, bs in enumerate(basis):
    for i, obs in enumerate(basis):
        A[i, j] = cal[bs].get(obs, 0) / total_counts

# Prepare observed noisy counts
n = np.array([noisy_counts.get(bs, 0) for bs in basis], dtype=float)

# Apply least-squares correction to get mitigated counts
x, *_ = np.linalg.lstsq(A, n, rcond=None)
mitigated_counts = {bs: round(max(0.0, xi)) for bs, xi in zip(basis, x)}

# Calculate mitigated probabilities and fidelity
mitigated_probs = {bs: count / total_counts for bs, count in mitigated_counts.items()}
mitigated_fidelity = hellinger_fidelity(ideal_probs, mitigated_probs)
mitigated_error_rate = 1 - mitigated_fidelity  # 1 - Mitigated Fidelity

# Diagnostics
print('Ideal Counts:', ideal_counts)
print('Noisy Counts:', noisy_counts)
print('Fidelity:', fidelity)
print('Error Rate:', error_rate)
print('Mitigated Counts:', mitigated_counts)
print('Mitigated Fidelity:', mitigated_fidelity)
print('Mitigated Error Rate:', mitigated_error_rate)

# Final output
output = {
    'ideal_counts': ideal_counts,
    'noisy_counts': noisy_counts,
    'fidelity': fidelity,
    'error_rate': error_rate,
    'mitigated_counts': mitigated_counts,
    'mitigated_fidelity': mitigated_fidelity,
    'mitigated_error_rate': mitigated_error_rate
}